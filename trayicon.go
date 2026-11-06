package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// digits is a 5x7 bitmap font, one string of rows for each digit.
var digits = [10][]string{
	{"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	{"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	{"01110", "10001", "00001", "00110", "01000", "10000", "11111"},
	{"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	{"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	{"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	{"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	{"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	{"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	{"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
}

// letters is a 3x5 bitmap font of the letters the labels use.
var letters = map[rune][]string{
	'A': {"010", "101", "111", "101", "101"},
	'C': {"111", "100", "100", "100", "111"},
	'G': {"111", "100", "101", "101", "111"},
	'M': {"101", "111", "101", "101", "101"},
	'P': {"111", "101", "111", "100", "100"},
	'R': {"110", "101", "110", "101", "101"},
	'U': {"101", "101", "101", "101", "111"},
}

var (
	grayColor = color.NRGBA{R: 110, G: 110, B: 110, A: 255}
	redColor  = color.NRGBA{R: 200, G: 40, B: 40, A: 255}
)

// tempColor is the background of a temperature, which tells at a glance how
// hot it is.
func tempColor(temp int, ok bool) color.NRGBA {
	switch {
	case !ok:
		return grayColor
	case temp >= 85:
		return redColor
	case temp >= 70:
		return color.NRGBA{R: 200, G: 110, B: 0, A: 255}
	}
	return color.NRGBA{R: 30, G: 130, B: 70, A: 255}
}

// usageColor makes the background of a usage: its own color, to tell the icons
// apart, until it is nearly full.
func usageColor(base color.NRGBA) func(int, bool) color.NRGBA {
	return func(use int, ok bool) color.NRGBA {
		switch {
		case !ok:
			return grayColor
		case use >= 90:
			return redColor
		}
		return base
	}
}

// drawBitmap draws rows of a bitmap font in white, each pixel a square of scale.
func drawBitmap(img *image.NRGBA, rows []string, left, top, scale int) {
	white := image.NewUniform(color.White)
	for y, row := range rows {
		for x, bit := range row {
			if bit == '1' {
				r := image.Rect(left+x*scale, top+y*scale, left+(x+1)*scale, top+(y+1)*scale)
				draw.Draw(img, r, white, image.Point{}, draw.Src)
			}
		}
	}
}

// trayIcon draws a label over a value of two digits, in white on a colored
// square, which shows on light and dark taskbars. Without a reading, it draws
// two dashes for the value.
func trayIcon(label string, value int, ok bool, background color.NRGBA) []byte {
	const size, labelScale, digitScale, digitsTop = 32, 2, 3, 11
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)

	const letterStep = 3*labelScale + 2
	left := (size - (len(label)*letterStep - 2)) / 2
	for i, r := range label {
		drawBitmap(img, letters[r], left+i*letterStep, 0, labelScale)
	}
	if ok {
		value = max(0, min(value, 99))
		for i, d := range [2]int{value / 10, value % 10} {
			drawBitmap(img, digits[d], 1+i*(5*digitScale+1), digitsTop, digitScale)
		}
	} else {
		white := image.NewUniform(color.White)
		draw.Draw(img, image.Rect(2, 19, 14, 23), white, image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(18, 19, 30, 23), white, image.Point{}, draw.Src)
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
