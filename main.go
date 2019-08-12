package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

var startFlag = flag.String("start", "", "skip all the photos more recent than the one at that URL")

var lastPhoto string

func main() {
	flag.Parse()
	s, err := NewSession()
	if err != nil {
		log.Fatal(err)
	}
	defer s.Shutdown()

	log.Printf("Dir: %v", s.tempDir)

	//	s.fixPreferences()

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
		//		chromedp.Sleep(30000*time.Millisecond),
		chromedp.Sleep(15000*time.Millisecond),
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

	download := func(ctx context.Context, dir string) error {
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
			log.Printf("Event: %+v", *ev)
			if err := ev.Do(ctx); err != nil {
				return err
			}
		}

		// TODO(mpl): use ioutil.Readdir.
		// Also haha, there is a .DS_Store file
		started := false
		endTimeout := time.Now().Add(30 * time.Second)
		startTimeout := time.Now().Add(5 * time.Second)
		tick := 500 * time.Millisecond
		for {
			foo := func() (bool, error) {
				dirfd, err := os.Open(dir)
				if err != nil {
					return false, err
				}
				defer dirfd.Close()
				time.Sleep(tick)
				println("TICK")
				if time.Now().After(endTimeout) {
					return false, fmt.Errorf("timeout while downloading in %q", dir)
				}
				if !started && time.Now().After(startTimeout) {
					return false, fmt.Errorf("downloading in %q took too long to start", dir)
				}
				entries, err := dirfd.Readdirnames(-1)
				if err != nil {
					return false, err
				}
				if len(entries) < 1 {
					return false, nil
				}
				if len(entries) > 1 {
					for _, v := range entries {
						println(v)
					}
					return false, fmt.Errorf("more than one file (%d) in download dir %q", len(entries), dir)
				}
				if !started {
					if len(entries) > 0 {
						started = true
					}
					return false, nil
				}
				println(entries[0])
				if !strings.HasSuffix(entries[0], ".crdownload") {
					// download is over
					return true, nil
				}
				return false, nil
			}
			done, err := foo()
			if err != nil {
				return err
			}
			if done {
				break
			}
		}
		return nil
	}

	//	firstNav := func(ctx context.Context) chromedp.ActionFunc {
	//		return func(ctx context.Context) error {
	firstNav := func(ctx context.Context) error {
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		log.Printf("sent key")
		chromedp.Sleep(500 * time.Millisecond).Do(ctx)
		chromedp.KeyEvent("\n").Do(ctx)
		chromedp.Sleep(500 * time.Millisecond).Do(ctx)
		return nil
	}
	//	}

	navRight := chromedp.ActionFunc(func(ctx context.Context) error {
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		log.Printf("sent key")
		chromedp.Sleep(500 * time.Millisecond).Do(ctx)
		return nil
	})

	navRightN := func(N int, ctx context.Context) chromedp.ActionFunc {
		n := 0
		return func(ctx context.Context) error {
			for {
				if n >= N {
					break
				}
				chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
				chromedp.Sleep(500 * time.Millisecond).Do(ctx)
				/*
					if err := navRight.Do(ctx); err != nil {
						return err
					}
					chromedp.Sleep(500 * time.Millisecond).Do(ctx)
					if err := download.Do(ctx); err != nil {
						return err
					}
				*/
				n++
			}
			return nil
		}
	}

	_, _, _ = download, navRight, navRightN

	if err := chromedp.Run(ctx,
		// TODO(mpl): change dl dir for each photo, to detect it's finished downloading.
		// TODO(mpl): add policy func over photo URL, which decides what we do (with?)
		page.SetDownloadBehavior(page.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(s.dlDir),
		chromedp.Navigate("https://photos.google.com/"),
		chromedp.Sleep(5000*time.Millisecond),
		// the `ERROR: unhandled page event *page.EventDownloadWillBegin` error does show up, but it does not actually prevent the download, so who cares?

		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("body is ready")
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := firstNav(ctx); err != nil {
				return err
			}
			return download(ctx, s.dlDir)
		}),

		chromedp.ActionFunc(func(ctx context.Context) error {
			dir, err := ioutil.TempDir(s.dlDir, "")
			if err != nil {
				return err
			}
			page.SetDownloadBehavior(page.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(dir)
			chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
			chromedp.Sleep(500 * time.Millisecond).Do(ctx)
			return download(ctx, dir)
		}),

		/*

			chromedp.ActionFunc(func(ctx context.Context) error {
				// TODO(mpl): instead of tempdir name, probably use dated name instead?
				dir, err := ioutil.TempDir(s.dlDir, "")
				if err != nil {
					return err
				}
				// TODO(mpl): that does not seem to be working here. maybe we need to do it before navigating?
				// Maybe if we put each nav+download into their own chromedp.Run ?
				page.SetDownloadBehavior(page.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(dir)
				// TODO(mpl): cleanup dir
				if err := firstNav(ctx); err != nil {
					return err
				}
				return download(ctx, dir)
			}),
			navRightN(5, ctx),
		*/
	); err != nil {
		log.Fatal(err)
	}
	fmt.Println("OK")

	// Next: keys
	// https://github.com/chromedp/chromedp/issues/400
	// https://godoc.org/github.com/chromedp/chromedp/kb

}

type Session struct {
	parentContext context.Context
	parentCancel  context.CancelFunc
	dlDir         string
	tempDir       string
}

func NewSession() (*Session, error) {
	/*
		dir, err := ioutil.TempDir("", "footest")
		if err != nil {
			return nil, err
		}
	*/
	dir := "/Users/mpl/chromedp"
	s := &Session{
		tempDir: dir,
		dlDir:   filepath.Join(os.Getenv("HOME"), "Downloads", "pk-gphotos"),
	}
	if err := os.MkdirAll(s.dlDir, 0700); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) NewContext() (context.Context, context.CancelFunc) {
	ctx, cancel := chromedp.NewExecAllocator(context.Background(),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(s.tempDir),

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
