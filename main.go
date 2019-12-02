/*
Copyright 2019 The Perkeep Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// The gphotos-cdp program uses the Chrome DevTools Protocol to drive a Chrome session
// that downloads your photos stored in Google Photos.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

var (
	// TODO(daneroo): New flags after -v: order if merge?
	nItemsFlag        = flag.Int("n", -1, "number of items to download. If negative, get them all.")
	devFlag           = flag.Bool("dev", false, "dev mode. we reuse the same session dir (/tmp/gphotos-cdp), so we don't have to auth at every run.")
	dlDirFlag         = flag.String("dldir", "", "where to write the downloads. defaults to $HOME/Downloads/gphotos-cdp.")
	startFlag         = flag.String("start", "", "skip all photos until this location is reached. for debugging.")
	runFlag           = flag.String("run", "", "the program to run on each downloaded item, right after it is dowloaded. It is also the responsibility of that program to remove the downloaded item, if desired.")
	verboseFlag       = flag.Bool("v", false, "be verbose")
	verboseTimingFlag = flag.Bool("vt", false, "be verbose about timing")
	listFlag          = flag.Bool("list", false, "Only list, do not download any images")
	allFlag           = flag.Bool("all", false, "Ignore -start and <dlDir>/.lastDone, start from oldest photo, implied by -list")
)

var tick = 500 * time.Millisecond

func main() {
	flag.Parse()
	if *nItemsFlag == 0 {
		return
	}
	if !*devFlag && *startFlag != "" {
		log.Print("-start only allowed in dev mode")
		return
	}
	if *listFlag {
		*allFlag = true
	}
	if *startFlag != "" && *allFlag {
		log.Print("-start is ignored if -all (implied by -list)")
		return
	}
	s, err := NewSession()
	if err != nil {
		log.Print(err)
		return
	}
	defer s.Shutdown()

	log.Printf("Session Dir: %v", s.profileDir)
	log.Printf("Download Dir: %v", s.dlDir)

	if err := s.cleanDlDir(); err != nil {
		log.Print(err)
		return
	}

	ctx, cancel := s.NewContext()
	defer cancel()

	if err := s.login(ctx); err != nil {
		log.Print(err)
		return
	}

	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(s.firstNav),
		chromedp.ActionFunc(s.navN(*nItemsFlag)),
	); err != nil {
		log.Print(err)
		return
	}
	fmt.Println("OK")
}

type Session struct {
	parentContext context.Context
	parentCancel  context.CancelFunc
	dlDir         string // dir where the photos get stored
	profileDir    string // user data session dir. automatically created on chrome startup.
	// lastDone is the most recent (wrt to Google Photos timeline) item (its URL
	// really) that was downloaded. If set, it is used as a sentinel, to indicate that
	// we should skip dowloading all items older than this one.
	lastDone string
}

// getLastDone returns the URL of the most recent item that was downloaded in
// the previous run. If any, it should have been stored in dlDir/.lastdone
func getLastDone(dlDir string) (string, error) {
	data, err := ioutil.ReadFile(filepath.Join(dlDir, ".lastdone"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func NewSession() (*Session, error) {
	var dir string
	if *devFlag {
		dir = filepath.Join(os.TempDir(), "gphotos-cdp")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	} else {
		var err error
		dir, err = ioutil.TempDir("", "gphotos-cdp")
		if err != nil {
			return nil, err
		}
	}
	dlDir := *dlDirFlag
	if dlDir == "" {
		dlDir = filepath.Join(os.Getenv("HOME"), "Downloads", "gphotos-cdp")
	}
	if err := os.MkdirAll(dlDir, 0700); err != nil {
		return nil, err
	}
	lastDone, err := getLastDone(dlDir)
	if err != nil {
		return nil, err
	}
	s := &Session{
		profileDir: dir,
		dlDir:      dlDir,
		lastDone:   lastDone,
	}
	return s, nil
}

func (s *Session) NewContext() (context.Context, context.CancelFunc) {
	ctx, cancel := chromedp.NewExecAllocator(context.Background(),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(s.profileDir),

		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("enable-features", "NetworkService,NetworkServiceInProcess"),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-features", "site-per-process,TranslateUI,BlinkGenPropertyTrees"),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-ipc-flooding-protection", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("force-color-profile", "srgb"),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("safebrowsing-disable-auto-update", true),
		chromedp.Flag("enable-automation", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),
	)
	s.parentContext = ctx
	s.parentCancel = cancel
	ctx, cancel = chromedp.NewContext(s.parentContext)
	return ctx, cancel
}

func (s *Session) Shutdown() {
	s.parentCancel()
}

// cleanDlDir removes all files (but not directories) from s.dlDir
func (s *Session) cleanDlDir() error {
	if s.dlDir == "" {
		return nil
	}
	entries, err := ioutil.ReadDir(s.dlDir)
	if err != nil {
		return err
	}
	for _, v := range entries {
		if v.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(s.dlDir, v.Name())); err != nil {
			return err
		}
	}
	return nil
}

// login navigates to https://photos.google.com/ and waits for the user to have
// authenticated (or for 2 minutes to have elapsed).
func (s *Session) login(ctx context.Context) error {
	return chromedp.Run(ctx,
		page.SetDownloadBehavior(page.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(s.dlDir),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if *verboseFlag {
				log.Printf("pre-navigate")
			}
			return nil
		}),
		chromedp.Navigate("https://photos.google.com/"),
		// when we're not authenticated, the URL is actually
		// https://www.google.com/photos/about/ , so we rely on that to detect when we have
		// authenticated.
		chromedp.ActionFunc(func(ctx context.Context) error {
			tick := time.Second
			timeout := time.Now().Add(2 * time.Minute)
			var location string
			for {
				if time.Now().After(timeout) {
					return errors.New("timeout waiting for authentication")
				}
				if err := chromedp.Location(&location).Do(ctx); err != nil {
					return err
				}
				if location == "https://photos.google.com/" {
					return nil
				}
				if *verboseFlag {
					log.Printf("Not yet authenticated, at: %v", location)
				}
				time.Sleep(tick)
			}
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if *verboseFlag {
				log.Printf("post-navigate")
			}
			return nil
		}),
	)
}

// firstNav does either of:
// 1) if a specific photo URL was specified with *startFlag, it navigates to it
// 2) if the last session marked what was the most recent downloaded photo, it navigates to it
// 3) otherwise it jumps to the end of the timeline (i.e. the oldest photo)
func (s *Session) firstNav(ctx context.Context) error {

	// fetch lastPhoto before bavigating to specific photoURL
	if err := lastPhoto(ctx); err != nil {
		return err
	}

	if *startFlag != "" {
		chromedp.Navigate(*startFlag).Do(ctx)
		chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
		return nil
	}
	if s.lastDone != "" {
		chromedp.Navigate(s.lastDone).Do(ctx)
		chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
		return nil
	}

	if err := navToEnd(ctx); err != nil {
		return err
	}

	if err := navToLast(ctx); err != nil {
		return err
	}

	return nil
}

func lastPhoto(ctx context.Context) error {
	// This should be our TerminationCriteria
	// extract most recent photo URL
	// expr := `document.querySelector('a[href^="./photo/"]').href;` // first photo
	// This selector matches <a href="./photo/*"/> elements
	// - where the ^= operator matches prefix of the href attribute
	sel := `a[href^="./photo/"]` // first photo on the landing page
	var href string
	ok := false
	if err := chromedp.AttributeValue(sel, "href", &href, &ok).Do(ctx); err != nil {
		return err
	}
	if !ok {
		return errors.New("firstNav: Unable to find most recent photo")
	}
	log.Printf("Last Photo: %s (first on Landing Page)", href)
	return nil
}

// navToEnd waits for the page to be ready to receive scroll key events, by
// trying to select an item with the right arrow key, and then scrolls down to the
// end of the page, i.e. to the oldest items.
func navToEnd(ctx context.Context) error {
	// wait for page to be loaded, i.e. that we can make an element active by using
	// the right arrow key.
	for {
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		time.Sleep(tick)
		var ids []cdp.NodeID
		if err := chromedp.Run(ctx,
			chromedp.NodeIDs(`document.activeElement`, &ids, chromedp.ByJSPath)); err != nil {
			return err
		}
		if len(ids) > 0 {
			if *verboseFlag {
				log.Printf("We are ready, because element %v is selected", ids[0])
			}
			break
		}
		time.Sleep(tick)
	}

	// try jumping to the end of the page. detect we are there and have stopped
	// moving when two consecutive screenshots are identical.
	var previousScr, scr []byte
	for {
		chromedp.KeyEvent(kb.PageDown).Do(ctx)
		chromedp.KeyEvent(kb.End).Do(ctx)
		chromedp.CaptureScreenshot(&scr).Do(ctx)
		if previousScr == nil {
			previousScr = scr
			continue
		}
		if bytes.Equal(previousScr, scr) {
			break
		}
		previousScr = scr
		time.Sleep(tick)
	}

	if *verboseFlag {
		log.Printf("Successfully jumped to the end")
	}

	return nil
}

// navToLast sends the "\n" event until we detect that an item is loaded as a
// new page. It then sends the right arrow key event until we've reached the very
// last item.
func navToLast(ctx context.Context) error {
	var location, prevLocation string
	ready := false
	for {
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		time.Sleep(tick)
		if !ready {
			chromedp.KeyEvent("\n").Do(ctx)
			time.Sleep(tick)
		}
		if err := chromedp.Location(&location).Do(ctx); err != nil {
			return err
		}
		if !ready {
			if location != "https://photos.google.com/" {
				ready = true
				log.Printf("Nav to the end sequence is started because location is %v", location)
			}
			continue
		}

		if location == prevLocation {
			break
		}
		log.Printf("NavToLast iteration: location is %v", location)
		prevLocation = location
	}
	return nil
}

// doRun runs *runFlag as a command on the given filePath.
func doRun(filePath string) error {
	if *runFlag == "" {
		return nil
	}
	if *verboseFlag {
		log.Printf("Running %v on %v", *runFlag, filePath)
	}
	cmd := exec.Command(*runFlag, filePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// navLeft navigates to the next item to the left
func navLeft(ctx context.Context, prevLocation *string) error {
	start := time.Now()
	deadline := start.Add(5 * time.Minute) // sleep max of 5 minutes
	maxBackoff := 500 * time.Millisecond
	backoff := 10 * time.Millisecond // exp backoff: 10,20,40,..,maxBackoff,maxBackoff,..

	// var prevLocation string
	var location string

	chromedp.KeyEvent(kb.ArrowLeft).Do(ctx)
	//  I don't think this actually waits for anything, body is already ready
	// ...usualy takes <10us
	// chromedp.WaitReady("body", chromedp.ByQuery)

	n := 0
	for {
		if err := chromedp.Location(&location).Do(ctx); err != nil {
			return err
		}
		n++
		if location != *prevLocation {
			break
		}

		// Should return an error, but this is conflated with Termination Condition
		if time.Now().After(deadline) {
			log.Printf("NavLeft timed out after: %s (%d)", time.Since(start), n)
			break
		}

		time.Sleep(backoff)
		if backoff < maxBackoff {
			// calculate next exponential backOff (capped to maxBackoff)
			backoff = backoff * 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
	elapsed := time.Since(start)
	if *verboseTimingFlag && (n > 10 || elapsed.Seconds() > 10) {
		log.Printf(". navLeft n:%d elapsed: %s", n, elapsed)
	}
	return nil
}

// markDone saves location in the dldir/.lastdone file, to indicate it is the
// most recent item downloaded
func markDone(dldir, location string) error {
	if *verboseFlag {
		log.Printf("Marking %v as done", location)
	}
	// TODO(mpl): back up .lastdone before overwriting it, in case writing it fails.
	if err := ioutil.WriteFile(filepath.Join(dldir, ".lastdone"), []byte(location), 0600); err != nil {
		return err
	}
	return nil
}

// startDownload sends the Shift+D event, to start the download of the currently
// viewed item.
func startDownload(ctx context.Context) error {
	keyD, ok := kb.Keys['D']
	if !ok {
		return errors.New("no D key")
	}

	down := input.DispatchKeyEventParams{
		Key:                   keyD.Key,
		Code:                  keyD.Code,
		NativeVirtualKeyCode:  keyD.Native,
		WindowsVirtualKeyCode: keyD.Windows,
		Type:                  input.KeyDown,
		Modifiers:             input.ModifierShift,
	}
	if runtime.GOOS == "darwin" {
		down.NativeVirtualKeyCode = 0
	}
	up := down
	up.Type = input.KeyUp

	for _, ev := range []*input.DispatchKeyEventParams{&down, &up} {
		if *verboseFlag {
			log.Printf("Event: %+v", *ev)
			// log.Printf("Event: %+v", (*ev).Type)
		}
		if err := ev.Do(ctx); err != nil {
			return err
		}
	}
	return nil
}

// dowload starts the download of the currently viewed item, and on successful
// completion saves its location as the most recent item downloaded. It returns
// with an error if the download stops making any progress for more than a minute.
func (s *Session) download(ctx context.Context, location string) (string, error) {

	if err := startDownload(ctx); err != nil {
		return "", err
	}

	var filename string
	started := false
	var fileSize int64
	deadline := time.Now().Add(time.Minute)
	for {
		time.Sleep(tick)
		if !started && time.Now().After(deadline) {
			return "", fmt.Errorf("downloading in %q took too long to start", s.dlDir)
		}
		if started && time.Now().After(deadline) {
			return "", fmt.Errorf("hit deadline while downloading in %q", s.dlDir)
		}

		entries, err := ioutil.ReadDir(s.dlDir)
		if err != nil {
			return "", err
		}
		var fileEntries []os.FileInfo
		for _, v := range entries {
			if v.IsDir() {
				continue
			}
			if v.Name() == ".lastdone" {
				continue
			}
			fileEntries = append(fileEntries, v)
		}
		if len(fileEntries) < 1 {
			continue
		}
		if len(fileEntries) > 1 {
			return "", fmt.Errorf("more than one file (%d) in download dir %q", len(fileEntries), s.dlDir)
		}
		if !started {
			if len(fileEntries) > 0 {
				started = true
				deadline = time.Now().Add(time.Minute)
			}
		}
		newFileSize := fileEntries[0].Size()
		if newFileSize > fileSize {
			// push back the timeout as long as we make progress
			deadline = time.Now().Add(time.Minute)
			fileSize = newFileSize
		}
		if !strings.HasSuffix(fileEntries[0].Name(), ".crdownload") {
			// download is over
			filename = fileEntries[0].Name()
			break
		}
	}

	if err := markDone(s.dlDir, location); err != nil {
		return "", err
	}

	return filename, nil
}

// moveDownload creates a directory in s.dlDir named of the item ID found in
// location. It then moves dlFile in that directory. It returns the new path
// of the moved file.
func (s *Session) moveDownload(ctx context.Context, dlFile, location string) (string, error) {
	parts := strings.Split(location, "/")
	if len(parts) < 5 {
		return "", fmt.Errorf("not enough slash separated parts in location %v: %d", location, len(parts))
	}
	newDir := filepath.Join(s.dlDir, parts[4])
	if err := os.MkdirAll(newDir, 0700); err != nil {
		return "", err
	}
	newFile := filepath.Join(newDir, dlFile)
	if err := os.Rename(filepath.Join(s.dlDir, dlFile), newFile); err != nil {
		return "", err
	}
	return newFile, nil
}

func (s *Session) dlAndMove(ctx context.Context, location string) (string, error) {
	dlFile, err := s.download(ctx, location)
	if err != nil {
		return "", err
	}
	return s.moveDownload(ctx, dlFile, location)
}

// navN successively downloads the currently viewed item, and navigates to the
// next item (to the left). It repeats N times or until the last (i.e. the most
// recent) item is reached. Set a negative N to repeat until the end is reached.
func (s *Session) navN(N int) func(context.Context) error {
	return func(ctx context.Context) error {
		n := 0
		if N == 0 {
			return nil
		}
		var location, prevLocation string
		totalDuration := 0.0
		batchDuration := 0.0
		batchSize := 1000

		for {
			start := time.Now()
			if err := chromedp.Location(&location).Do(ctx); err != nil {
				return err
			}
			if location == prevLocation {
				break
			}
			prevLocation = location

			if !*listFlag {
				filePath, err := s.dlAndMove(ctx, location)
				if err != nil {
					return err
				}
				if err := doRun(filePath); err != nil {
					return err
				}
			} else {
				if *verboseFlag {
					log.Printf("Listing (%d): %v", n, location)
				}
			}

			n++
			if N > 0 && n >= N {
				break
			}
			// better termination - print rate befaore leaving?
			if err := navLeft(ctx, &prevLocation); err != nil {
				return err
			}
			totalDuration += time.Since(start).Seconds()
			batchDuration += time.Since(start).Seconds()
			if n%batchSize == 0 {

				// This is where we reload the page - this reduces accumulated latency significantly
				// Page reload taked about 1.3 seconds
				reloadStart := time.Now()
				if err := chromedp.Reload().Do(ctx); err != nil {
					if *verboseTimingFlag {
						log.Printf(". Avg Latency (last %d @ %d):  Marginal:%.2fms Cumulative: %.2fms",
							batchSize, n, batchDuration*1000.0/float64(batchSize), totalDuration*1000.0/float64(n))
					}
					log.Printf("Failed to reload Page at n:%d", n)

					return err
				}

				if *verboseTimingFlag {
					log.Printf(". Avg Latency (last %d @ %d):  Marginal:%.2fms Cumulative: %.2fms Page Reloaded:%s",
						batchSize, n, batchDuration*1000.0/float64(batchSize), totalDuration*1000.0/float64(n), time.Since(reloadStart))
				}
				if *verboseFlag {
					log.Printf("Reloaded page at: %s in %s", location, time.Since(reloadStart))
				}
				batchDuration = 0.0 // reset Statistics
			}
		}
		if *verboseTimingFlag {
			log.Printf("Rate (%d): %.2f/s Avg Latency: %.2fms", n, float64(n)/totalDuration, totalDuration*1000.0/float64(n))
		}
		return nil
	}
}
