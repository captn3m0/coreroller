package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"

	// this MUST not reference any godo/v? directory

	"github.com/mgutz/minimist"
	"gopkg.in/godo.v2"
	"gopkg.in/godo.v2/util"
	"gopkg.in/godo.v2/watcher"
)

var isWindows = runtime.GOOS == "windows"
var isRebuild bool
var isWatch bool
var isVerbose bool
var hasTasks bool

func checkError(err error, format string, args ...interface{}) {
	if err != nil {
		util.Error("ERR", format, args...)
		os.Exit(1)
	}
}

func hasMain(data []byte) bool {
	hasMainRe := regexp.MustCompile(`\nfunc main\(`)
	matches := hasMainRe.Find(data)
	return len(matches) > 0
}

func isPackageMain(data []byte) bool {
	isMainRe := regexp.MustCompile(`(\n|^)?package main\b`)
	matches := isMainRe.Find(data)
	return len(matches) > 0
}

func main() {
	// v2 ONLY uses Gododir/main.go
	godoFiles := []string{"Gododir/main.go", "Gododir/Godofile.go", "tasks/Godofile.go"}
	src := ""
	for _, filename := range godoFiles {
		src = util.FindUp(".", filename)
		if src != "" {
			break
		}
	}

	if src == "" {
		fmt.Printf("\n\n%s not found\n", src)
		os.Exit(1)
	}

	wd, err := os.Getwd()
	if err != nil {
		util.Error("godo", "Could not get working directory: %s\n", err.Error())
	}

	// parent of Gododir/main.go
	absParentDir, err := filepath.Abs(filepath.Dir(filepath.Dir(src)))
	if err != nil {
		util.Error("godo", "Could not get absolute parent of %s: %s\n", src, err.Error())
	}
	if wd != absParentDir {
		relDir, _ := filepath.Rel(wd, src)
		os.Chdir(absParentDir)
		util.Info("godo", "Using %s\n", relDir)
	}

	os.Setenv("GODOFILE", src)
	argm := minimist.Parse()
	isRebuild = argm.AsBool("rebuild")
	isWatch = argm.AsBool("w", "watch")
	isVerbose = argm.AsBool("v", "verbose")
	hasTasks = len(argm.NonFlags()) > 0
	run(src)
}

func run(godoFile string) {
	if isWatch {
		runAndWatch(godoFile)
	} else {
		cmd, _ := buildCommand(godoFile, false)
		err := cmd.Run()
		if err != nil {
			log.Fatal(err)
		}
	}
}

func buildCommand(godoFile string, forceBuild bool) (*exec.Cmd, string) {
	exe := buildMain(godoFile, forceBuild)
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd, exe
}

func runAndWatch(godoFile string) {
	done := make(chan bool, 1)
	run := func(forceBuild bool) (*exec.Cmd, string) {
		cmd, exe := buildCommand(godoFile, forceBuild)
		cmd.Start()
		go func() {
			err := cmd.Wait()
			done <- true
			if err != nil {
				if isVerbose {
					util.Debug("godo", "godo process killed\n")
				}
			}
		}()
		return cmd, exe
	}

	bufferSize := 2048
	watchr, err := watcher.NewWatcher(bufferSize)
	if err != nil {
		util.Panic("project", "%v\n", err)
	}
	godoDir := filepath.Dir(godoFile)
	watchr.WatchRecursive(godoDir)
	watchr.ErrorHandler = func(err error) {
		util.Error("godo", "Watcher error %v\n", err)
	}

	cmd, exe := run(false)
	// this function will block forever, Ctrl+C to quit app
	// var lastHappenedTime int64
	watchr.Start()
	util.Info("godo", "watching %s ...\n", godoDir)

	<-time.After(godo.GetWatchDelay() + (300 * time.Millisecond))

	// forloop:
	for {
		select {
		case event := <-watchr.Event:
			if event.Path == exe {
				continue
			}
			util.Debug("watchmain", "%+v\n", event)
			syscall.Kill(cmd.Process.Pid, syscall.SIGQUIT)
			cmd.Process.Kill()
			<-done
			cmd, _ = run(true)
		}
	}

}

func mustBeMain(src string) {
	data, err := ioutil.ReadFile(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if !hasMain(data) {
		msg := `%s is not runnable. Rename package OR make it runnable by adding

	func main() {
		godo.Godo(tasks)
	}
	`
		fmt.Printf(msg, src)
		os.Exit(1)
	}

	if !isPackageMain(data) {
		msg := `%s is not runnable. It must be package main`
		fmt.Printf(msg, src)
		os.Exit(1)
	}
}

func buildMain(src string, forceBuild bool) string {
	mustBeMain(src)
	dir := filepath.Dir(src)

	exeFile := "godobin-" + godo.Version
	if isWindows {
		exeFile += ".exe"
	}

	exe := filepath.Join(dir, exeFile)

	build := false
	reasonFormat := ""
	if isRebuild || forceBuild {
		build = true
		reasonFormat = "Rebuilding %s...\n"
	} else {
		build = util.Outdated([]string{dir + "/**/*.go"}, []string{exe})
		reasonFormat = "Godo tasks changed. Rebuilding %s...\n"
	}

	if build {
		util.Debug("godo", reasonFormat, exe)
		_, err := godo.Run("go build -a -o "+exeFile, godo.M{"$in": dir})
		if err != nil {
			panic(fmt.Sprintf("Error building %s: %s\n", src, err.Error()))
		}
		// for some reason go build does not delete the exe named after the dir
		// which ends up with Gododir/Gododir
		if filepath.Base(dir) == "Gododir" {
			orphanedFile := filepath.Join(dir, filepath.Base(dir))
			if _, err := os.Stat(orphanedFile); err == nil {
				os.Remove(orphanedFile)
			}
		}
	}

	if isRebuild {
		util.Info("godo", "ok\n")
	}

	return exe
}
