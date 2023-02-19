package main

import (
	"github.com/theone-daxia/progressbar"
	"time"
)

func main() {
	bar := progressbar.Default(50)
	for i := 0; i < 50; i++ {
		_ = bar.Add(1)
		time.Sleep(40 * time.Millisecond)
	}
}
