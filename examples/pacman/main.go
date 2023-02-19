package main

import (
	"fmt"
	"time"

	"github.com/k0kubun/go-ansi"
	"github.com/theone-daxia/progressbar"
)

func main() {
	doneCh := make(chan struct{})

	bar := progressbar.NewOptions64(1000,
		progressbar.OptionSetWriter(ansi.NewAnsiStdout()),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionSetWidth(50),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        " ",
			AltSaucerHead: "[yellow]<[reset]",
			SaucerHead:    "[yellow]-[reset]",
			SaucerPadding: "[white]•",
			BarStart:      "[blue]|[reset]",
			BarEnd:        "[blue]|[reset]",
		}),
		progressbar.OptionOnCompletion(func() {
			doneCh <- struct{}{}
		}),
	)

	go func() {
		for i := 0; i < 1000; i++ {
			bar.Add(1)
			time.Sleep(10 * time.Millisecond)
		}
	}()

	<-doneCh
	fmt.Println("\n ===== progress bar completed ===== \n")
}
