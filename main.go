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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

var (
	nItemsFlag   = flag.Int("n", -1, "number of items to download. If negative, get them all.")
	devFlag      = flag.Bool("dev", false, "dev mode. we reuse the same session dir (/tmp/gphotos-cdp), so we don't have to auth at every run.")
	dlDirFlag    = flag.String("dldir", "", "where to write the downloads. defaults to $HOME/Downloads/gphotos-cdp.")
	startFlag    = flag.String("start", "", "skip all photos until this location is reached. for debugging.")
	runFlag      = flag.String("run", "", "the program to run on each downloaded item, right after it is dowloaded. It is also the responsibility of that program to remove the downloaded item, if desired.")
	verboseFlag  = flag.Bool("v", false, "be verbose")
	headlessFlag = flag.Bool("headless", false, "Start chrome browser in headless mode (cannot do authentication this way).")
)

var tick = 500 * time.Millisecond

func main() {
	flag.Parse()
	if *nItemsFlag == 0 {
		return
	}
	if !*devFlag && *startFlag != "" {
		log.Fatal("-start only allowed in dev mode")
	}
	if !*devFlag && *headlessFlag {
		log.Fatal("-headless only allowed in dev mode")
	}
	s, err := NewSession()
	if err != nil {
		log.Fatal(err)
	}
	defer s.Shutdown()

	log.Printf("Session Dir: %v", s.profileDir)

	if err := s.cleanDlDir(); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := s.NewContext()
	defer cancel()

	if err := s.login(ctx); err != nil {
		log.Fatal(err)
	}

	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(s.firstNav),
		chromedp.ActionFunc(s.navN(*nItemsFlag)),
	); err != nil {
		log.Fatal(err)
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
	// firstItem is the most recent item in the feed. It is determined at the
	// beginning of the run, and is used as the final sentinel.
	firstItem string
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
	// Let's use as a base for allocator options (It implies Headless)
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.DisableGPU,
		chromedp.UserDataDir(s.profileDir),
	)

	if !*headlessFlag {
		// undo the three opts in chromedp.Headless() which is included in DefaultExecAllocatorOptions
		opts = append(opts, chromedp.Flag("headless", false))
		opts = append(opts, chromedp.Flag("hide-scrollbars", false))
		opts = append(opts, chromedp.Flag("mute-audio", false))
		// undo DisableGPU from above
		opts = append(opts, chromedp.Flag("disable-gpu", false))
	}
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
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
		if v.Name() == ".lastdone" {
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
		browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(s.dlDir),
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
				if *headlessFlag {
					return errors.New("authentication not possible in -headless mode")
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
	if err := s.setFirstItem(ctx); err != nil {
		return err
	}

	if *startFlag != "" {
		// TODO(mpl): use RunResponse
		chromedp.Navigate(*startFlag).Do(ctx)
		chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
		return nil
	}
	if s.lastDone != "" {
		resp, err := chromedp.RunResponse(ctx, chromedp.Navigate(s.lastDone))
		if err != nil {
			return err
		}
		if resp.Status == http.StatusOK {
			chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
			return nil
		}
		lastDoneFile := filepath.Join(s.dlDir, ".lastdone")
		log.Printf("%s does not seem to exist anymore. Removing %s.", s.lastDone, lastDoneFile)
		s.lastDone = ""
		if err := os.Remove(lastDoneFile); err != nil {
			if os.IsNotExist(err) {
				log.Fatal("Failed to remove .lastdone file because it was already gone.")
			}
			return err
		}

		// restart from scratch
		resp, err = chromedp.RunResponse(ctx, chromedp.Navigate("https://photos.google.com/"))
		if err != nil {
			return err
		}
		code := resp.Status
		if code != http.StatusOK {
			return fmt.Errorf("unexpected %d code when restarting to https://photos.google.com/", code)
		}
		chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
	}

	if err := navToEnd(ctx); err != nil {
		return err
	}

	if err := navToLast(ctx); err != nil {
		return err
	}

	return nil
}

// setFirstItem looks for the first item, and sets it as s.firstItem.
// We always run it first even for code paths that might not need s.firstItem,
// because we also run it for the side-effect of waiting for the first page load to
// be done, and to be ready to receive scroll key events.
func (s *Session) setFirstItem(ctx context.Context) error {
	// wait for page to be loaded, i.e. that we can make an element active by using
	// the right arrow key.
	for {
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		time.Sleep(tick)
		attributes := make(map[string]string)
		if err := chromedp.Run(ctx,
			chromedp.Attributes(`document.activeElement`, &attributes, chromedp.ByJSPath)); err != nil {
			return err
		}
		if len(attributes) == 0 {
			time.Sleep(tick)
			continue
		}

		photoHref, ok := attributes["href"]
		if !ok || !strings.HasPrefix(photoHref, "./photo/") {
			time.Sleep(tick)
			continue
		}

		s.firstItem = strings.TrimPrefix(photoHref, "./photo/")
		break
	}
	if *verboseFlag {
		log.Printf("Page loaded, most recent item in the feed is: %s", s.firstItem)
	}
	return nil
}

// navToEnd scrolls down to the end of the page, i.e. to the oldest items.
func navToEnd(ctx context.Context) error {
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
		time.Sleep(10*tick)
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
func navLeft(ctx context.Context) error {
	var res []string
	chromedp.EvaluateAsDevTools(`window.resize();`, &res)
	time.Sleep(tick)
	muNavWaiting.Lock()
	listenEvents = true
	muNavWaiting.Unlock()
	chromedp.KeyEvent(kb.ArrowLeft).Do(ctx)
	muNavWaiting.Lock()
	navWaiting = true
	muNavWaiting.Unlock()
	t := time.NewTimer(time.Minute)
	select {
	case <-navDone:
		if !t.Stop() {
			<-t.C
		}
	case <-t.C:
		return errors.New("timeout waiting for left navigation")
	}
	muNavWaiting.Lock()
	navWaiting = false
	muNavWaiting.Unlock()
	return nil
}

// markDone saves location in the dldir/.lastdone file, to indicate it is the
// most recent item downloaded
func markDone(dldir, location string) error {
	if *verboseFlag {
		log.Printf("Marking %v as done", location)
	}
	oldPath := filepath.Join(dldir, ".lastdone")
	newPath := oldPath + ".bak"
	if err := os.Rename(oldPath, newPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}
	if err := ioutil.WriteFile(oldPath, []byte(location), 0600); err != nil {
		// restore from backup
		if err := os.Rename(newPath, oldPath); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
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
			if v.Name() == ".lastdone.bak" {
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

var (
	muNavWaiting             sync.RWMutex
	listenEvents, navWaiting = false, false
	navDone                  = make(chan bool, 1)
)

func listenNavEvents(ctx context.Context) {
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		muNavWaiting.RLock()
		listen := listenEvents
		muNavWaiting.RUnlock()
		if !listen {
			return
		}
		switch ev.(type) {
		case *page.EventNavigatedWithinDocument:
			go func() {
				for {
					muNavWaiting.RLock()
					waiting := navWaiting
					muNavWaiting.RUnlock()
					if waiting {
						navDone <- true
						break
					}
					time.Sleep(tick)
				}
			}()
		}
	})
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

		listenNavEvents(ctx)

		var location, prevLocation string
		for {
			if err := chromedp.Location(&location).Do(ctx); err != nil {
				return err
			}
			if location == prevLocation {
				break
			}
			prevLocation = location
			filePath, err := s.dlAndMove(ctx, location)
			if err != nil {
				return err
			}
			if err := doRun(filePath); err != nil {
				return err
			}
			n++
			if N > 0 && n >= N {
				break
			}
			if strings.HasSuffix(location, s.firstItem) {
				break
			}

			if err := navLeft(ctx); err != nil {
				return fmt.Errorf("error at %v: %v", location, err)
			}
		}
		return nil
	}
}
