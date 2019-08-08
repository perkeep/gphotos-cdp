package main

import (
	"context"
	"encoding/json"
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
	"go4.org/lock"
)

var startFlag = flag.String("start", "", "skip all the photos more recent than the one at that URL")

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
		chromedp.Sleep(30000*time.Millisecond),
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

	download := chromedp.ActionFunc(func(ctx context.Context) error {
		dir, err := ioutil.TempDir(s.dlDir, "")
		if err != nil {
			return err
		}
		page.SetDownloadBehavior(page.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(dir)
		// TODO(mpl): cleanup dir

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

		dirfd, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer dirfd.Close()
		timeout := time.Now().Add(30 * time.Second)
		for {
			if time.Now().After(timeout) {
				return fmt.Errorf("timeout while downloading in %q", dir)
			}
			entries, err := dirfd.Readdirnames(-1)
			if err != nil {
				return err
			}
			if len(entries) != 1 {
				return fmt.Errorf("more or less than one file (%d) in download dir %q", len(entries), dir)
			}
			if !strings.HasSuffix(entries[0], ".crdownload") {
				// download is over
				break
			}
		}
		return nil
	})

	navRight := chromedp.ActionFunc(func(ctx context.Context) error {
		chromedp.KeyEvent(kb.ArrowRight).Do(ctx)
		log.Printf("sent key")
		chromedp.Sleep(500 * time.Millisecond).Do(ctx)
		chromedp.KeyEvent("\n").Do(ctx)
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
				if err := navRight.Do(ctx); err != nil {
					return err
				}
				chromedp.Sleep(500 * time.Millisecond).Do(ctx)
				if err := download.Do(ctx); err != nil {
					return err
				}
				chromedp.Sleep(5 * time.Second)
				n++
			}
			return nil
		}
	}
	if err := chromedp.Run(ctx,
		// TODO(mpl): change dl dir for each photo, to detect it's finished downloading.
		// TODO(mpl): add policy func over photo URL, which decides what we do (with?)
		//		page.SetDownloadBehavior(page.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(s.dlDir),
		chromedp.Navigate("https://photos.google.com/"),
		chromedp.Sleep(5000*time.Millisecond),
		// the `ERROR: unhandled page event *page.EventDownloadWillBegin` error does show up, but it does not actually prevent the download, so who cares?

		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("body is ready")
			return nil
		}),
		navRightN(5, ctx),
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
	dir, err := ioutil.TempDir("", "footest")
	if err != nil {
		return nil, err
	}
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
		//chromedp.UserDataDir("/Users/bradfitz/.config/perkeep/google-photos-chrome-profile"),
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

func (s *Session) fixPreferences() {
	ctx, _ := s.NewContext()
	//	defer cancel()

	// Looks like go4.org/lock does not work to test against SingletonLock
	println("WE SHOULD HAVE A LOCK ON " + filepath.Join(s.tempDir, "SingletonLock"))
	if err := chromedp.Run(ctx,
		chromedp.Navigate("chrome://settings/"),
		//		chromedp.Sleep(100*time.Millisecond),
		chromedp.Sleep(time.Minute),
	); err != nil {
		panic(err)
	}

	var prefPath string
	t0 := time.Now()
	var lastLog time.Time
	for prefPath == "" {
		filepath.Walk(s.tempDir, func(path string, fi os.FileInfo, err error) error {
			if filepath.Base(path) == "Preferences" {
				//				cancel()
				log.Printf("%s", path)
				prefPath = path
				return nil
			}
			return nil
		})
		time.Sleep(50 * time.Millisecond)
		if lastLog.Before(time.Now().Add(-1 * time.Second)) {
			log.Printf("waiting for preference file to be written")
			lastLog = time.Now()
		}
	}
	//	cancel()
	if err := chromedp.Cancel(ctx); err != nil {
		log.Fatalf("Error when cancelling: %v", err)
	}
	s.Shutdown()
	log.Printf("Got pref file %s after %v", prefPath, time.Since(t0).Round(25*time.Millisecond))
	//	time.Sleep(time.Minute)
	for {
		break
		if cl, err := lock.Lock(filepath.Join(s.tempDir, "SingletonLock")); err == nil {
			defer cl.Close()
			println("got LOCK on ", filepath.Join(s.tempDir, "SingletonLock"))
			time.Sleep(time.Minute)
			break
		}
		time.Sleep(time.Second)
		println("waiting on lock file")
	}
	return

	dlDir := filepath.Join(s.tempDir, "Downloads")
	log.Printf("Download dir: %v", dlDir)
	if err := os.MkdirAll(dlDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := updateDownloadDir(prefPath, dlDir); err != nil {
		panic(err)
	}
}

func updateDownloadDir(prefFile, dlDir string) error {
	oldData, err := ioutil.ReadFile(prefFile)
	if err != nil {
		return err
	}

	preferences := make(map[string]interface{})
	if err := json.Unmarshal(oldData, &preferences); err != nil {
		return err
	}

	for _, k := range []string{"download", "savefile"} {
		if _, ok := preferences[k]; !ok {
			preferences[k] = map[string]interface{}{}
		}
		jo := preferences[k].(map[string]interface{})
		jo["default_directory"] = dlDir
		if k == "download" {
			jo["directory_upgrade"] = true
		}
	}

	newData, err := json.Marshal(preferences)
	if err != nil {
		return err
	}
	println(newData)
	//	return ioutil.WriteFile(prefFile, newData, 0600)
	return ioutil.WriteFile(prefFile, oldData, 0600)
}
