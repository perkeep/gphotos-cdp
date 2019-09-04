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
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

var (
	nItemsFlag = flag.Int("n", -1, "number of items to download. If negative, get them all.")
	devFlag    = flag.Bool("dev", false, "dev mode. we reuse the same session dir (/tmp/gphotos-cdp), so we don't have to auth at every run.")
	dlDirFlag  = flag.String("dldir", "", "where to (temporarily) write the downloads. defaults to $HOME/Downloads/gphotos-cdp.")
	startFlag  = flag.String("start", "", "skip all photos until this location is reached. for debugging.")
	runFlag    = flag.String("run", "", "the program to run on each downloaded item, right after it is dowloaded. It is also the responsibility of that program to remove the downloaded item, if desired.")
)

// TODO(mpl): in general everywhere, do not rely so much on sleeps. We need
// better ways to wait for things to be loaded/ready.

func main() {
	flag.Parse()
	if *nItemsFlag == 0 {
		return
	}
	if !*devFlag && *startFlag != "" {
		log.Fatal("-start only allowed in dev mode")
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

	var outerBefore string

	// login phase
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("pre-navigate")
			return nil
		}),
		chromedp.Navigate("https://photos.google.com/"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if *devFlag {
				//				chromedp.Sleep(5*time.Second).Do(ctx)
				chromedp.Sleep(15 * time.Second).Do(ctx)
			} else {
				chromedp.Sleep(30 * time.Second).Do(ctx)
			}
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("post-navigate")
			return nil
		}),
		chromedp.OuterHTML("html>body", &outerBefore),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("Source is %d bytes", len(outerBefore))
			return nil
		}),
	); err != nil {
		log.Fatal(err)
	}

	firstNav := func(ctx context.Context) error {
		if *startFlag != "" {
			chromedp.Navigate(*startFlag).Do(ctx)
			chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
			chromedp.Sleep(5000 * time.Millisecond).Do(ctx)
			return nil
		}
		if s.lastDone != "" {
			chromedp.Navigate(s.lastDone).Do(ctx)
			chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
			chromedp.Sleep(5000 * time.Millisecond).Do(ctx)
			return nil
		}
		// For some reason, I need to do a pagedown before, for the end key to work...
		chromedp.KeyEvent(kb.PageDown).Do(ctx)
		chromedp.Sleep(500 * time.Millisecond).Do(ctx)
		chromedp.KeyEvent(kb.End).Do(ctx)
		chromedp.Sleep(5000 * time.Millisecond).Do(ctx)
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		chromedp.Sleep(500 * time.Millisecond).Do(ctx)
		chromedp.KeyEvent("\n").Do(ctx)
		chromedp.Sleep(time.Second).Do(ctx)
		var location, prevLocation string
		if err := chromedp.Location(&prevLocation).Do(ctx); err != nil {
			return err
		}
		for {
			chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
			chromedp.Sleep(time.Second).Do(ctx)
			if err := chromedp.Location(&location).Do(ctx); err != nil {
				return err
			}
			if location == prevLocation {
				break
			}
			prevLocation = location
		}
		return nil
	}

	markDone := func(dldir, location string) error {
		println("LOCATION: ", location)
		// TODO(mpl): back up .lastdone before overwriting it, in case writing it fails.
		if err := ioutil.WriteFile(filepath.Join(dldir, ".lastdone"), []byte(location), 0600); err != nil {
			return err
		}
		return nil
	}

	download := func(ctx context.Context, location string) (string, error) {
		dir := s.dlDir
		keyD, ok := kb.Keys['D']
		if !ok {
			log.Fatal("NO D KEY")
		}

		down := input.DispatchKeyEventParams{
			Key:  keyD.Key,
			Code: keyD.Code,
			// Some github issue says to remove NativeVirtualKeyCode, but it does not change anything.
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
			//			log.Printf("Event: %+v", *ev)
			if err := ev.Do(ctx); err != nil {
				return "", err
			}
		}

		var filename string
		started := false
		tick := 500 * time.Millisecond
		var fileSize int64
		timeout := time.Now().Add(time.Minute)
		for {
			time.Sleep(tick)
			// TODO(mpl): download starts late if it's a video. figure out if dl can only
			// start after video has started playing or something like that?
			if !started && time.Now().After(timeout) {
				return "", fmt.Errorf("downloading in %q took too long to start", dir)
			}
			if started && time.Now().After(timeout) {
				return "", fmt.Errorf("timeout while downloading in %q", dir)
			}

			entries, err := ioutil.ReadDir(dir)
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
				return "", fmt.Errorf("more than one file (%d) in download dir %q", len(fileEntries), dir)
			}
			if !started {
				if len(fileEntries) > 0 {
					started = true
					timeout = time.Now().Add(time.Minute)
				}
			}
			newFileSize := fileEntries[0].Size()
			if newFileSize > fileSize {
				// push back the timeout as long as we make progress
				timeout = time.Now().Add(time.Minute)
				fileSize = newFileSize
			}
			if !strings.HasSuffix(fileEntries[0].Name(), ".crdownload") {
				// download is over
				filename = fileEntries[0].Name()
				break
			}
		}

		if err := markDone(dir, location); err != nil {
			return "", err
		}

		return filename, nil
	}

	mvDl := func(dlFile, location string) func(ctx context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
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
	}

	dlAndMove := func(ctx context.Context, location string) (string, error) {
		var err error
		dlFile, err := download(ctx, location)
		if err != nil {
			return "", err
		}
		return mvDl(dlFile, location)(ctx)
	}

	doRun := func(ctx context.Context, filePath string) error {
		if *runFlag == "" {
			return nil
		}
		return exec.Command(*runFlag, filePath).Run()
	}

	navRight := func(ctx context.Context) error {
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		chromedp.WaitReady("body", chromedp.ByQuery)
		chromedp.Sleep(1 * time.Second).Do(ctx)
		return nil
	}

	navLeft := func(ctx context.Context) error {
		chromedp.KeyEvent(kb.ArrowLeft).Do(ctx)
		chromedp.WaitReady("body", chromedp.ByQuery)
		chromedp.Sleep(1 * time.Second).Do(ctx)
		return nil
	}

	navN := func(direction string, N int) func(context.Context) error {
		n := 0
		return func(ctx context.Context) error {
			if direction != "left" && direction != "right" {
				return errors.New("wrong direction, pun intended")
			}
			if N == 0 {
				return nil
			}
			var location, prevLocation string

			for {
				if err := chromedp.Location(&location).Do(ctx); err != nil {
					return err
				}
				if location == prevLocation {
					break
				}
				prevLocation = location
				filePath, err := dlAndMove(ctx, location)
				if err != nil {
					return err
				}
				// TODO(mpl): do run in a go routine?
				if err := doRun(ctx, filePath); err != nil {
					return err
				}
				n++
				if N > 0 && n >= N {
					break
				}
				if direction == "right" {
					if err := navRight(ctx); err != nil {
						return err
					}
				} else {
					if err := navLeft(ctx); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}

	if err := chromedp.Run(ctx,
		page.SetDownloadBehavior(page.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(s.dlDir),
		chromedp.Navigate("https://photos.google.com/"),
		chromedp.Sleep(5000*time.Millisecond),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("body is ready")
			return nil
		}),
		chromedp.ActionFunc(firstNav),
		chromedp.ActionFunc(navN("left", *nItemsFlag)),
	); err != nil {
		log.Fatal(err)
	}
	fmt.Println("OK")
}

type Session struct {
	parentContext context.Context
	parentCancel  context.CancelFunc
	dlDir         string
	profileDir    string
	lastDone      string
}

func getLastDone(dlDir string) (string, error) {
	data, err := ioutil.ReadFile(filepath.Join(dlDir, ".lastdone"))
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		return "", nil
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
