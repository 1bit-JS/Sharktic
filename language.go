package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// The app's text is in English or Korean, English unless the user chose
// Korean. Each text is written in place in both, with tr or msg, so that a
// change of one cannot leave the other behind.

type language int32

const (
	english language = iota
	korean
)

// languageCodes are what the settings file keeps of each language.
var languageCodes = map[language]string{english: "en", korean: "ko"}

// currentLanguage is set on the main thread, and read anywhere.
var currentLanguage atomic.Int32

func appLanguage() language { return language(currentLanguage.Load()) }

func setAppLanguage(l language) { currentLanguage.Store(int32(l)) }

// msg is a text in each of the app's languages.
type msg struct{ en, ko string }

// String returns the text in the app's language.
func (m msg) String() string {
	if appLanguage() == korean {
		return m.ko
	}
	return m.en
}

// tr returns the text of the app's language of the two.
func tr(en, ko string) string { return msg{en, ko}.String() }

// languagePath is the file that keeps the language chosen.
func languagePath() string { return configPath("language") }

// loadLanguage reads the language chosen, English if none was.
func loadLanguage(path string) language {
	data, err := os.ReadFile(path)
	if err != nil {
		return english
	}
	code := strings.TrimSpace(string(data))
	for l, c := range languageCodes {
		if c == code {
			return l
		}
	}
	return english
}

func saveLanguage(path string, l language) {
	if path == "" {
		return
	}
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		err = os.WriteFile(path, []byte(languageCodes[l]+"\n"), 0o644)
	}
	if err != nil {
		log.Println("language:", err)
	}
}
