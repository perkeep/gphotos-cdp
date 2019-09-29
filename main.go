// This program uses the Chrome DevTools Protocol to drive a Chrome session that
// downloads your photos stored in Google Photos.
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
	nItemsFlag  = flag.Int("n", -1, "number of items to download. If negative, get them all.")
	devFlag     = flag.Bool("dev", false, "dev mode. we reuse the same session dir (/tmp/gphotos-cdp), so we don't have to auth at every run.")
	dlDirFlag   = flag.String("dldir", "", "where to write the downloads. defaults to $HOME/Downloads/gphotos-cdp.")
	startFlag   = flag.String("start", "", "skip all photos until this location is reached. for debugging.")
	runFlag     = flag.String("run", "", "the program to run on each downloaded item, right after it is dowloaded. It is also the responsibility of that program to remove the downloaded item, if desired.")
	verboseFlag = flag.Bool("v", false, "be verbose")
)

// TODO(mpl): in general everywhere, do not rely so much on sleeps. We need
// better ways to wait for things to be loaded/ready.

func main() {
	flag.Parse()
	if *nItemsFlag == 0 {
		return
	}
	if !*devFlag && *startFlag != "" {
		log.Print("-start only allowed in dev mode")
		return
	}
	s, err := NewSession()
	if err != nil {
		log.Print(err)
		return
	}
	defer s.Shutdown()

	log.Printf("Session Dir: %v", s.profileDir)

	if err := s.cleanDlDir(); err != nil {
		log.Print(err)
		return
	}

	ctx, cancel := s.NewContext()
	defer cancel()

	if err := login(ctx); err != nil {
		log.Print(err)
		return
	}

	if err := chromedp.Run(ctx,
		page.SetDownloadBehavior(page.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(s.dlDir),
		chromedp.Navigate("https://photos.google.com/"),
		chromedp.Sleep(5000*time.Millisecond),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if *verboseFlag {
				log.Printf("body is ready")
			}
			return nil
		}),
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
func login(ctx context.Context) error {
	var outerBefore string
	return chromedp.Run(ctx,
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
			time.Sleep(time.Second)
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
				time.Sleep(time.Second)
			}
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if *verboseFlag {
				log.Printf("post-navigate")
			}
			return nil
		}),
		chromedp.OuterHTML("html>body", &outerBefore),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if *verboseFlag {
				log.Printf("Source is %d bytes", len(outerBefore))
			}
			return nil
		}),
	)
}

// firstNav does either of:
// 1) if a specific photo URL was specified with *startFlag, it navigates to it
// 2) if the last session marked what was the most recent downloaded photo, it navigates to it
// 3) otherwise it jumps to the end of the timeline (i.e. the oldest photo)
func (s Session) firstNav(ctx context.Context) error {
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
	chromedp.KeyEvent(kb.ArrowLeft).Do(ctx)
	chromedp.WaitReady("body", chromedp.ByQuery)
	chromedp.Sleep(1 * time.Second).Do(ctx)
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
func (s Session) download(ctx context.Context, location string) (string, error) {

	if err := startDownload(ctx); err != nil {
		return "", err
	}

	var filename string
	started := false
	tick := 500 * time.Millisecond
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
// location. It then moves dlFile in that directory.
func (s Session) moveDownload(ctx context.Context, dlFile, location string) (string, error) {
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

func (s Session) dlAndMove(ctx context.Context, location string) (string, error) {
	dlFile, err := s.download(ctx, location)
	if err != nil {
		return "", err
	}
	return s.moveDownload(ctx, dlFile, location)
}

// navN successively downloads the currently viewed item, and navigates to the
// next item (to the left). It repeats N times or until the last (i.e. the most
// recent) item is reached. Set a negative N to repeat until the end is reached.
func (s Session) navN(N int) func(context.Context) error {
	return func(ctx context.Context) error {
		n := 0
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
			filePath, err := s.dlAndMove(ctx, location)
			if err != nil {
				return err
			}
			// TODO(mpl): do run in a go routine?
			if err := doRun(filePath); err != nil {
				return err
			}
			n++
			if N > 0 && n >= N {
				break
			}
			if err := navLeft(ctx); err != nil {
				return err
			}
		}
		return nil
	}
}
