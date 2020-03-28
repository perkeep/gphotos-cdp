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
	"sync"
	"time"

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

	return nil
}

// navToEnd selects the last item in the page
//  by repeatedly advancing the selected item with
//  - kb.ArrowRight (which causes an initial selection, and/or advances it by one)
//  - kb.End which scrolls to the end of the page, and advances the selected item.
// Note timing is important, because when the kb.End causes significant scrolling,
// the active element become undefined for a certain time, in that case, we
// get an error (ignore), sleep, and retry.
// The termnation criteria is that the selected item (document.activeElement.href)
// is stable for >2 iterations
func navToEnd(ctx context.Context) error {
	var prev, active string
	lastRepeated := 0
	for {
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		chromedp.KeyEvent(kb.End).Do(ctx)
		time.Sleep(tick)

		if err := chromedp.Evaluate(`document.activeElement.href`, &active).Do(ctx); err != nil {
			// This extra sleep is important: after the kb.End,
			// it sometimes takes a while for the scrolled page to be in a state
			// which allows the next kb.ArrowRight to take effect and actually select
			// the next element at the new scroll position.
			time.Sleep(tick)
			continue // ignore this error: no active element, or active element has no href
		}
		if active == prev {
			lastRepeated++
		} else {
			lastRepeated = 0
		}
		if *verboseFlag {
			log.Printf("Active element %s was seen %d times", active, lastRepeated+1)
		}
		if lastRepeated > 2 {
			break
		}
		prev = active
	}

	chromedp.KeyEvent("\n").Do(ctx)
	time.Sleep(tick)
	var location string
	if err := chromedp.Location(&location).Do(ctx); err != nil {
		return err
	}

	if active == location {
		if *verboseFlag {
			log.Printf("Successfully jumped to the end: %s", location)
		}
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
				if *verboseFlag {
					log.Printf("Terminating because we stopped advancing: %s", prevLocation)
				}
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
				if *verboseFlag {
					log.Printf("Terminating because desired number of items (%d) was reached", n)
				}
				break
			}

			if err := navLeft(ctx); err != nil {
				return fmt.Errorf("error at %v: %v", location, err)
			}
		}
		return nil
	}
}
