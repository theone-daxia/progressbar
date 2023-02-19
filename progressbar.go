package progressbar

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/mitchellh/colorstring"
	"golang.org/x/term"
)

type ProgressBar struct {
	state  state
	config config
	lock   sync.Mutex
}

// State is the basic properties of the bar
type State struct {
	CurrentPercent float64
	CurrentBytes   float64
	SecondsSince   float64
	SecondsLeft    float64
	KBsPerSecond   float64
}

type state struct {
	currentNum        int64
	currentPercent    int
	lastPercent       int
	currentSaucerSize int
	isAltSaucerHead   bool

	startTime time.Time
	lastShown time.Time

	counterTime         time.Time
	counterNumSinceLast int64
	counterLastTenRates []float64

	maxLineWidth int
	currentBytes float64
	finished     bool
	exit         bool // progress bar exit halfway

	rendered string
}

type config struct {
	max                  int64 // max number of the counter
	maxHumanized         string
	maxHumanizedSuffix   string
	width                int
	writer               io.Writer
	theme                Theme
	renderWithBlankState bool
	description          string
	ignoreLength         bool // ignoreLength if max bytes not known

	// minimum time to wait in between updates
	throttleDuration time.Duration

	// spinnerType should be a number between 0-75
	spinnerType int

	// whether the progress bar should show elapsed time.
	// always enabled if predictTime is true.
	elapsedTime bool

	// whether the progress bar should attempt to predict the finishing
	// time of the progress based on the start time and the average
	// number of seconds between increments.
	predictTime bool

	// show rate of change in KB/sec or MB/sec
	showBytes bool

	// show currentNum/totalNum or currentNum
	showIterationsCount bool

	// showDescriptionAtLineEnd specifies whether description should be written at line end instead of line start
	showDescriptionAtLineEnd bool

	onCompletion func()

	// fullWidth specifies whether to measure and set the bar to a specific width
	fullWidth bool

	// whether the render function should make use of ANSI codes to reduce console I/O
	useANSICodes bool

	// clear bar once finished
	clearOnFinish bool

	// whether the output is expected to contain color codes
	colorCodes bool
}

// Theme defines the elements of the bar
type Theme struct {
	Saucer        string
	AltSaucerHead string
	SaucerHead    string
	SaucerPadding string
	BarStart      string
	BarEnd        string
}

// Option is the type all options need to adhere to
type Option func(p *ProgressBar)

// OptionSetWidth sets the width of the bar
func OptionSetWidth(width int) Option {
	return func(p *ProgressBar) {
		p.config.width = width
	}
}

// OptionSpinnerType sets the type of spinner used for indeterminate bars
func OptionSpinnerType(spinnerType int) Option {
	return func(p *ProgressBar) {
		p.config.spinnerType = spinnerType
	}
}

// OptionSetTheme sets the elements the bar is constructed of
func OptionSetTheme(t Theme) Option {
	return func(p *ProgressBar) {
		p.config.theme = t
	}
}

// OptionSetDescription sets the description of the bar to render in front of it
func OptionSetDescription(description string) Option {
	return func(p *ProgressBar) {
		p.config.description = description
	}
}

// OptionSetWriter sets the output writer (defaults to os.Stdout)
func OptionSetWriter(w io.Writer) Option {
	return func(p *ProgressBar) {
		p.config.writer = w
	}
}

// OptionShowBytes will update the progress bar
// configuration settings to display/hide KBytes/Sec
func OptionShowBytes(val bool) Option {
	return func(p *ProgressBar) {
		p.config.showBytes = val
	}
}

// OptionThrottle will wait the specified duration before updating again.
// The default duration is 0 seconds.
func OptionThrottle(duration time.Duration) Option {
	return func(p *ProgressBar) {
		p.config.throttleDuration = duration
	}
}

// OptionShowCount will also print current count out of total
func OptionShowCount() Option {
	return func(p *ProgressBar) {
		p.config.showIterationsCount = true
	}
}

// OptionOnCompletion will invoke cmpl function once its finished
func OptionOnCompletion(cmpl func()) Option {
	return func(p *ProgressBar) {
		p.config.onCompletion = cmpl
	}
}

// OptionFullWidth sets the bar to be full width
func OptionFullWidth() Option {
	return func(p *ProgressBar) {
		p.config.fullWidth = true
	}
}

// OptionSetRenderBlankState sets whether to render a 0% bar on construction
func OptionSetRenderBlankState(r bool) Option {
	return func(p *ProgressBar) {
		p.config.renderWithBlankState = r
	}
}

// OptionEnableColorCodes enables or disables support for color codes
// using mitchellh/colorstring
func OptionEnableColorCodes(colorCodes bool) Option {
	return func(p *ProgressBar) {
		p.config.colorCodes = colorCodes
	}
}

var defaultTheme = Theme{Saucer: "█", SaucerPadding: " ", BarStart: "|", BarEnd: "|"}

func NewOptions64(max int64, options ...Option) *ProgressBar {
	b := ProgressBar{
		state: getBasicState(),
		config: config{
			writer:           os.Stdout,
			theme:            defaultTheme,
			width:            40,
			max:              max,
			throttleDuration: 0 * time.Nanosecond,
			elapsedTime:      true,
			predictTime:      true,
			spinnerType:      9,
		},
	}

	for _, o := range options {
		o(&b)
	}

	if b.config.spinnerType < 0 || b.config.spinnerType > 75 {
		panic("invalid spinner type, must be between 0 and 75")
	}

	// ignoreLength if max bytes not known
	if b.config.max == -1 {
		b.config.ignoreLength = true
		b.config.max = int64(b.config.width)
		b.config.predictTime = false
	}

	b.config.maxHumanized, b.config.maxHumanizedSuffix = humanizeBytes(float64(b.config.max))

	if b.config.renderWithBlankState {
		b.RenderBlank()
	}

	return &b
}

func getBasicState() state {
	now := time.Now()
	return state{
		startTime:   now,
		lastShown:   now,
		counterTime: now,
	}
}

// Default provides a progress bar with recommended defaults.
// Set max to -1 to use as a spinner.
func Default(max int64, description ...string) *ProgressBar {
	desc := ""
	if len(description) > 0 {
		desc = description[0]
	}

	return NewOptions64(
		max,
		OptionSetDescription(desc),
		OptionSetWriter(os.Stderr),
		OptionShowBytes(true),
		OptionSetWidth(10),
		OptionThrottle(65*time.Millisecond),
		OptionShowCount(),
		OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		OptionSpinnerType(14),
		OptionFullWidth(),
		OptionSetRenderBlankState(true),
	)
}

// String returns the current rendered version of the progress bar.
// It will never return an empty string while the progress bar is running.
func (p *ProgressBar) String() string {
	return p.state.rendered
}

// Add will add the specified amount to the progress bar
func (p *ProgressBar) Add(num int) error {
	return p.Add64(int64(num))
}

// Add64 will add the specified amount to the progress bar
func (p *ProgressBar) Add64(num int64) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	if p.state.exit {
		return nil
	}

	if p.config.max == 0 {
		return errors.New("max must be greater than 0")
	}

	if p.state.currentNum < p.config.max {
		if p.config.ignoreLength {
			p.state.currentNum = (p.state.currentNum + num) % p.config.max
		} else {
			p.state.currentNum += num
		}
	}

	p.state.currentBytes += float64(num)

	// reset the countdown timer every second to take rolling average
	p.state.counterNumSinceLast += num
	if time.Since(p.state.counterTime).Seconds() > 0.5 {
		p.state.counterLastTenRates = append(p.state.counterLastTenRates, float64(p.state.counterNumSinceLast)/time.Since(p.state.counterTime).Seconds())
		if len(p.state.counterLastTenRates) > 10 {
			p.state.counterLastTenRates = p.state.counterLastTenRates[1:]
		}
		p.state.counterTime = time.Now()
		p.state.counterNumSinceLast = 0
	}

	percent := float64(p.state.currentNum) / float64(p.config.max)
	p.state.currentSaucerSize = int(percent * float64(p.config.width))
	p.state.currentPercent = int(percent * 100)
	updateBar := p.state.currentPercent != p.state.lastPercent && p.state.currentPercent > 0

	p.state.lastPercent = p.state.currentPercent
	if p.state.currentNum > p.config.max {
		return errors.New("current number exceeds max")
	}

	// always update if show bytes/second or its/second
	if updateBar || p.config.showIterationsCount {
		return p.render()
	}

	return nil
}

// RenderBlank renders the current bar state, you can use this to render a 0% state
func (p *ProgressBar) RenderBlank() error {
	if p.state.currentNum == 0 {
		p.state.lastShown = time.Time{}
	}
	return p.render()
}

// Render renders the progress bar, updating the maximum
// rendered line width. this function is not thread-safe,
// so it must be called with an acquired lock.
func (p *ProgressBar) render() error {
	// make sure that the rendering is not happening too quickly
	// but always show if the currentNum reaches the max
	if time.Since(p.state.lastShown).Nanoseconds() < p.config.throttleDuration.Nanoseconds() &&
		p.state.currentNum < p.config.max {
		return nil
	}

	if !p.config.useANSICodes {
		// first, clear the existing progress bar
		err := clearProgressBar(p.config, p.state)
		if err != nil {
			return err
		}
	}

	// check if the progress bar is finished
	if !p.state.finished && p.state.currentNum >= p.config.max {
		p.state.finished = true
		if !p.config.clearOnFinish {
			renderProgressBar(p.config, &p.state)
		}
		if p.config.onCompletion != nil {
			p.config.onCompletion()
		}
	}
	if p.state.finished {
		// when using ANSI codes we don't pre-clean the current line
		if p.config.useANSICodes && p.config.clearOnFinish {
			err := clearProgressBar(p.config, p.state)
			if err != nil {
				return err
			}
		}
		return nil
	}

	// then, re-render the current progress bar
	w, err := renderProgressBar(p.config, &p.state)
	if err != nil {
		return err
	}

	if w > p.state.maxLineWidth {
		p.state.maxLineWidth = w
	}
	p.state.lastShown = time.Now()

	return nil
}

// 这个方法关掉也会正常输出
// 因为渲染进度条的时候，输出字符串会在开头添加一个 \r
// see: renderProgressBar()
func clearProgressBar(c config, s state) error {
	if s.maxLineWidth == 0 {
		return nil
	}
	if c.useANSICodes {
		// write the "clear current line" ANSI escape sequence
		return writeString(c, "\033[2K\r")
	}
	// fill the empty content
	// to overwrite the progress bar
	// and jump back to the beginning of the line
	str := fmt.Sprintf("\r%s\r", strings.Repeat(" ", s.maxLineWidth))
	return writeString(c, str)
}

func writeString(c config, str string) error {
	if _, err := io.WriteString(c.writer, str); err != nil {
		return err
	}

	if f, ok := c.writer.(*os.File); ok {
		// ignore any errors in Sync(), as stdout
		// can't be synced on some operating systems
		// like Debian 9 (Stretch)
		f.Sync()
	}

	return nil
}

func renderProgressBar(c config, s *state) (int, error) {
	var sb strings.Builder

	averageRate := average(s.counterLastTenRates)
	if len(s.counterLastTenRates) == 0 || s.finished {
		// if no average samples, or if finished,
		// then average rate should be the total rate.
		if t := time.Since(s.startTime).Seconds(); t > 0 {
			averageRate = s.currentBytes / t
		} else {
			averageRate = 0
		}
	}

	// show iteration count in "current/total" iterations format
	if c.showIterationsCount {
		if sb.Len() == 0 {
			sb.WriteString("(")
		} else {
			sb.WriteString(", ")
		}
		if !c.ignoreLength {
			if c.showBytes {
				currentHumanize, currentSuffix := humanizeBytes(s.currentBytes)
				if currentSuffix == c.maxHumanizedSuffix {
					sb.WriteString(fmt.Sprintf("%s/%s%s",
						currentHumanize, c.maxHumanized, c.maxHumanizedSuffix))
				} else {
					sb.WriteString(fmt.Sprintf("%s%s/%s%s",
						currentHumanize, currentSuffix, c.maxHumanized, c.maxHumanizedSuffix))
				}
			} else {
				sb.WriteString(fmt.Sprintf("%.0f/%d", s.currentBytes, c.max))
			}
		} else {
			if c.showBytes {
				currentHumanize, currentSuffix := humanizeBytes(s.currentBytes)
				sb.WriteString(fmt.Sprintf("%s%s", currentHumanize, currentSuffix))
			} else {
				sb.WriteString(fmt.Sprintf("%.0f/%s", s.currentBytes, "-"))
			}
		}
	}

	// show rolling average rate
	if c.showBytes && averageRate > 0 && !math.IsInf(averageRate, 1) {
		if sb.Len() == 0 {
			sb.WriteString("(")
		} else {
			sb.WriteString(", ")
		}
		currentHumanize, currentSuffix := humanizeBytes(averageRate)
		sb.WriteString(fmt.Sprintf("%s%s/s", currentHumanize, currentSuffix))
	}

	if sb.Len() > 0 {
		sb.WriteString(")")
	}

	leftBrac := ""   // 靠近左括号的内容（目前经过了多少秒）
	rightBrac := ""  // 靠近右括号的内容（目前还需多少秒）
	saucer := ""     // 进度条前进的填充物（原意：茶碟、碟状物）TODO 不知道为啥用这个单词
	saucerHead := "" //

	// show time prediction in "current/total" seconds format
	switch {
	case c.predictTime:
		rightBracNum := time.Duration((1/averageRate)*(float64(c.max)-float64(s.currentNum))) * time.Second
		if rightBracNum.Seconds() < 0 {
			rightBracNum = 0 * time.Second
		}
		rightBrac = rightBracNum.String()
		fallthrough
	case c.elapsedTime:
		leftBrac = (time.Duration(time.Since(s.startTime).Seconds()) * time.Second).String()
	}

	if c.fullWidth && !c.ignoreLength {
		width, err := termWidth()
		if err != nil {
			width = 80
		}

		amend := 1 // an extra space at eol
		switch {
		case leftBrac != "" && rightBrac != "":
			amend = 4 // space, square brackets and colon
		case leftBrac != "" && rightBrac == "":
			amend = 4 // space, square brackets and another space
		case leftBrac == "" && rightBrac != "":
			amend = 3 // space and square brackets
		}
		if c.showDescriptionAtLineEnd {
			amend += 1 // another space
		}

		c.width = width - getStringWidth(c, c.description, true) - 10 - amend - sb.Len() - len(leftBrac) - len(rightBrac)
		s.currentSaucerSize = int(float64(s.currentPercent) / 100.0 * float64(c.width))
	}
	if s.currentSaucerSize > 0 {
		if c.ignoreLength {
			saucer = strings.Repeat(c.theme.SaucerPadding, s.currentSaucerSize-1)
		} else {
			saucer = strings.Repeat(c.theme.Saucer, s.currentSaucerSize-1)
		}

		// check if an alternate saucer head is set for animation
		if c.theme.AltSaucerHead != "" && s.isAltSaucerHead {
			saucerHead = c.theme.AltSaucerHead
			s.isAltSaucerHead = false
		} else if c.theme.SaucerHead == "" || s.currentSaucerSize == c.width {
			// use the saucer for the saucer head if it hasn't been set
			// to preserve backwards compatibility
			saucerHead = c.theme.Saucer
		} else {
			saucerHead = c.theme.SaucerHead
			s.isAltSaucerHead = true
		}
	}

	/*
		Progress Bar format
		Description % |------    | (kb/s) (iteration count) (iteration rate) (predict time)

		or if showDescriptionAtLineEnd is enabled
		% |------    | (kb/s) (iteration count) (iteration rate) (predict time) Description
	*/

	repeatAmount := c.width - s.currentSaucerSize
	if repeatAmount < 0 {
		repeatAmount = 0
	}

	str := ""

	if c.ignoreLength {
		spinner := spinners[c.spinnerType][int(math.Round(math.Mod(float64(time.Since(s.startTime).Milliseconds()/100), float64(len(spinners[c.spinnerType])))))]
		if c.elapsedTime {
			if c.showDescriptionAtLineEnd {
				str = fmt.Sprintf("\r%s %s [%s] %s ",
					spinner,
					sb.String(),
					leftBrac,
					c.description)
			} else {
				str = fmt.Sprintf("\r%s %s %s [%s] ",
					spinner,
					c.description,
					sb.String(),
					leftBrac)
			}
		} else {
			if c.showDescriptionAtLineEnd {
				str = fmt.Sprintf("\r%s %s %s ",
					spinner,
					sb.String(),
					c.description)
			} else {
				str = fmt.Sprintf("\r%s %s %s ",
					spinner,
					c.description,
					sb.String())
			}
		}
	} else if rightBrac == "" {
		str = fmt.Sprintf("%4d%% %s%s%s%s%s %s",
			s.currentPercent,
			c.theme.BarStart,
			saucer,
			saucerHead,
			strings.Repeat(c.theme.SaucerPadding, repeatAmount),
			c.theme.BarEnd,
			sb.String())

		if c.showDescriptionAtLineEnd {
			str = fmt.Sprintf("\r%s %s ", str, c.description)
		} else {
			str = fmt.Sprintf("\r%s%s ", c.description, str)
		}
	} else {
		if s.currentPercent == 100 {
			str = fmt.Sprintf("%4d%% %s%s%s%s%s %s",
				s.currentPercent,
				c.theme.BarStart,
				saucer,
				saucerHead,
				strings.Repeat(c.theme.SaucerPadding, repeatAmount),
				c.theme.BarEnd,
				sb.String())
		} else {
			str = fmt.Sprintf("%4d%% %s%s%s%s%s %s [%s:%s]",
				s.currentPercent,
				c.theme.BarStart,
				saucer,
				saucerHead,
				strings.Repeat(c.theme.SaucerPadding, repeatAmount),
				c.theme.BarEnd,
				sb.String(),
				leftBrac,
				rightBrac)
		}

		if c.showDescriptionAtLineEnd {
			str = fmt.Sprintf("\r%s %s", str, c.description)
		} else {
			str = fmt.Sprintf("\r%s%s", c.description, str)
		}
	}

	if c.colorCodes {
		// convert any color codes in the progress bar into the respective ANSI codes
		str = colorstring.Color(str)
	}

	s.rendered = str

	return getStringWidth(c, str, false), writeString(c, str)
}

func humanizeBytes(s float64) (string, string) {
	sizes := []string{" B", " KB", " MB", " GB", " TB", " PB", " EB"}
	base := 1024.0
	if s < 10 {
		return fmt.Sprintf("%2.0f", s), sizes[0]
	}
	e := math.Floor(logn(s, base))
	suffix := sizes[int(e)]
	val := math.Floor(float64(s)/math.Pow(base, e)*10+0.5) / 10
	f := "%.0f"
	if val < 10 {
		f = "%.1f"
	}

	return fmt.Sprintf(f, val), suffix
}

func logn(n, b float64) float64 {
	return math.Log(n) / math.Log(b)
}

func average(xs []float64) float64 {
	total := 0.0
	for _, v := range xs {
		total += v
	}
	return total / float64(len(xs))
}

func termWidth() (width int, err error) {
	width, _, err = term.GetSize(int(os.Stdout.Fd()))
	if err == nil {
		return width, nil
	}
	width, _, err = term.GetSize(int(os.Stderr.Fd()))
	if err == nil {
		return width, nil
	}
	return 0, nil
}

// regex matching ansi escape codes
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func getStringWidth(c config, str string, colorize bool) int {
	if c.colorCodes {
		// convert any color codes int the progress bar into the respective ANSI codes
		str = colorstring.Color(str)
	}

	// the width of the string, if printed to the console
	// does not include the carriage return character
	cleanString := strings.Replace(str, "\r", "", -1)

	if c.colorCodes {
		// the ANSI codes for the colors do not take up space in the console output,
		// so they do not count towards the output string width
		cleanString = ansiRegex.ReplaceAllString(cleanString, "")
	}

	// get the amount of runes in the string instead of the
	// character count of the string, as some runes span multiple characters.
	// see https://stackoverflow.com/a/12668840/2733724
	stringWidth := runewidth.StringWidth(cleanString)
	return stringWidth
}
