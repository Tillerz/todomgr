package main

import "encoding/json"

// Defaults for a new install, in one place. Change them here to build the program with other
// defaults; then run ./build.sh. They only apply where nothing is saved yet: a data/config.json
// or a setting chosen in the app always wins, so existing installs keep their settings.
// (The version number is not here: build.sh and release.sh read it from main.go.)

// ---------------------------------------------------------------- program (data/config.json)

const (
	DefaultPort              = 7437 // http://localhost:7437/ ; also `todo serve --port`
	DefaultBackupIntervalMin = 10   // a snapshot every N minutes when something changed (1–1440)

	// Off by default on purpose. Switching one on opens a way into the data, so think twice
	// before a build turns it on for everyone.
	DefaultLANEnabled   = false // other devices in the network may connect (with the token)
	DefaultMCPEnabled   = false // AI assistants may connect (from this computer only)
	DefaultMCPReadOnly  = false // when MCP is on: AI assistants can read but not change anything
	DefaultCalendarFeed = false // offer the calendar subscription link (Settings → Calendar subscription)
	DefaultAIEnabled    = false // the AI buttons; also needs llama-server and a model in the ai folder (Settings → AI)

	// The longest attachment the AI reads, in characters (20,000 is about 8 pages). More needs more memory
	// while the AI runs and takes longer to read; "aiMaxChars" in data/config.json changes it per install.
	DefaultAIMaxChars = 20000
)

// ---------------------------------------------------------------- page (Settings → Appearance and more)

// uiDefaultsJSON: the page's settings until they are changed in the app. The page gets them from the
// program (/api/state → info.defaults). Keys:
//
//	style      theme: "" (Default), "modern", "pastel", "fantasy", "scifi", "contrast"
//	theme      "light", "dark", or "" to follow the system
//	font       a CSS font list such as "Georgia,serif", or "" for the theme's font
//	fontSize   text size in px (11–20), or 0 for the theme's size
//	rowH       row height in the tree, in px (28–64)
//	detailW    width of the detail panel, in px (300–760)
//	wide       true: the tree and the pages use the whole window; false: at most about 1000 px wide
//	detailFull true: the detail panel always shows all fields; false: fields without a value are behind "More fields"
//	workDay    the work day that Today's day plan compares with, "HH:MM"
//	tagIcons   tag → icon shown before the name; icon names as in Settings → Tag icons
const uiDefaultsJSON = `{
  "style": "",
  "theme": "",
  "font": "",
  "fontSize": 0,
  "rowH": 36,
  "detailW": 420,
  "wide": false,
  "detailFull": false,
  "cheer": true,
  "workDay": {"from": "09:00", "to": "17:00"},
  "tagIcons": {"meeting": "users", "stream": "video", "nsfw": "adult", "call": "phone", "mail": "mail",
    "bug": "bug", "research": "search", "review": "eye"}
}`

var uiDefaults = func() map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(uiDefaultsJSON), &m); err != nil {
		panic("defaults.go: uiDefaultsJSON is not valid JSON: " + err.Error())
	}
	return m
}()
