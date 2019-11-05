/*
 * Bamboo - A Vietnamese Input method editor
 * Copyright (C) 2018 Luong Thanh Lam <ltlam93@gmail.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BambooEngine/bamboo-core"
	"github.com/BambooEngine/goibus/ibus"
	"github.com/godbus/dbus"
)

var dictionary map[string]bool
var emojiTrie *TrieNode

func GetBambooEngineCreator() func(*dbus.Conn, string) dbus.ObjectPath {
	objectPath := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/IBus/Engine/bamboo/%d", time.Now().UnixNano()))
	setupConfigDir()
	go keyPressCapturing()
	engineName := strings.ToLower(EngineName)
	dictionary = map[string]bool{}
	emojiTrie = NewTrie()

	return func(conn *dbus.Conn, ngName string) dbus.ObjectPath {
		var engine = new(IBusBambooEngine)
		var config = loadConfig(engineName)
		var inputMethod = bamboo.ParseInputMethod(config.InputMethodDefinitions, config.InputMethod)
		engine.Engine = ibus.BaseEngine(conn, objectPath)
		engine.engineName = engineName
		engine.preeditor = bamboo.NewEngine(inputMethod, config.Flags)
		engine.config = loadConfig(engineName)
		engine.propList = GetPropListByConfig(config)
		ibus.PublishEngine(conn, objectPath, engine)
		go engine.init()

		return objectPath
	}
}

const KEYPRESS_DELAY_MS = 10

func (e *IBusBambooEngine) init() {
	e.emoji = NewEmojiEngine()
	if e.macroTable == nil {
		e.macroTable = NewMacroTable()
		if e.config.IBflags&IBmarcoEnabled != 0 {
			e.macroTable.Enable(e.engineName)
		}
	}
	if e.config.IBflags&IBspellCheckingWithDicts != 0 && len(dictionary) == 0 {
		dictionary, _ = loadDictionary(DictVietnameseCm)
	}
	if e.config.IBflags&IBemojiDisabled == 0 && emojiTrie != nil && len(emojiTrie.Children) == 0 {
		emojiTrie, _ = loadEmojiOne(DictEmojiOne)
	}
	keyPressHandler = e.keyPressHandler

	if e.config.IBflags&IBmouseCapturing != 0 {
		startMouseCapturing()
	}
	startMouseRecording()
	onMouseMove = func() {
		e.Lock()
		defer e.Unlock()
		if e.checkInputMode(preeditIM) {
			if e.getRawKeyLen() == 0 {
				return
			}
			e.commitPreedit(e.getPreeditString())
		}
	}
	onMouseClick = func() {
		e.Lock()
		defer e.Unlock()
		e.isFirstTimeSendingBS = true
		if e.isEmojiLTOpened {
			e.refreshEmojiCandidate()
		} else {
			e.resetFakeBackspace()
			e.resetBuffer()
			e.keyPressDelay = KEYPRESS_DELAY_MS
			if e.capabilities&IBUS_CAP_SURROUNDING_TEXT != 0 {
				//e.ForwardKeyEvent(IBUS_Shift_R, XK_Shift_R-8, 0)
				x11SendShiftR()
				e.isSurroundingTextReady = true
				e.keyPressDelay = KEYPRESS_DELAY_MS * 10
			}
		}
	}
	for i, list := range e.getWhiteList() {
		for _, wmClasses := range list {
			e.config.InputModeTable[wmClasses] = i + 1
		}
	}
	e.config.PreeditWhiteList = nil
	e.config.SLForwardKeyWhiteList = nil
	e.config.SurroundingTextWhiteList = nil
	e.config.DirectForwardKeyWhiteList = nil
	e.config.X11ClipboardWhiteList = nil
	e.config.ExceptedList = nil
	e.config.ForwardKeyWhiteList = nil
	saveConfig(e.config, e.engineName)
}

var keyPressHandler = func(keyVal, keyCode, state uint32) {}
var keyPressChan = make(chan [3]uint32, 100)

func keyPressCapturing() {
	for {
		select {
		case keyEvents := <-keyPressChan:
			var keyVal, keyCode, state = keyEvents[0], keyEvents[1], keyEvents[2]
			keyPressHandler(keyVal, keyCode, state)
		}
	}
}

func (e *IBusBambooEngine) resetBuffer() {
	if e.getRawKeyLen() == 0 {
		return
	}
	if e.checkInputMode(preeditIM) {
		e.commitPreedit(e.getPreeditString())
	} else {
		e.preeditor.Reset()
	}
}

func (e *IBusBambooEngine) processShiftKey(keyVal, state uint32) bool {
	if keyVal == IBUS_Shift_L || keyVal == IBUS_Shift_R {
		// when press one Shift key
		if state&IBUS_SHIFT_MASK != 0 && state&IBUS_RELEASE_MASK != 0 &&
			e.config.IBflags&IBimQuickSwitchEnabled != 0 && !e.lastKeyWithShift {
			e.englishMode = !e.englishMode
			notify(e.englishMode)
			e.resetBuffer()
		}
		return true
	}
	return false
}

func (e *IBusBambooEngine) updateLastKeyWithShift(keyVal, state uint32) {
	if e.canProcessKey(keyVal, state) {
		e.lastKeyWithShift = state&IBUS_SHIFT_MASK != 0
	} else {
		e.lastKeyWithShift = false
	}
}

func (e *IBusBambooEngine) isIgnoredKey(keyVal, state uint32) bool {
	if state&IBUS_RELEASE_MASK != 0 {
		//Ignore key-up event
		return true
	}
	if keyVal == IBUS_Caps_Lock {
		return true
	}
	if e.checkInputMode(usIM) {
		if e.isInputModeLTOpened || keyVal == IBUS_OpenLookupTable {
			return false
		}
		return true
	}
	return false
}

func (e *IBusBambooEngine) getRawKeyLen() int {
	return len(e.preeditor.GetRawString())
}

func (e *IBusBambooEngine) getInputMode() int {
	if e.wmClasses != "" {
		if im, ok := e.config.InputModeTable[e.wmClasses]; ok && imLookupTable[im] != "" {
			return im
		}
	}
	if imLookupTable[e.config.DefaultInputMode] != "" {
		return e.config.DefaultInputMode
	}
	return preeditIM
}

func (e *IBusBambooEngine) openLookupTable() {
	var wmClasses = strings.Split(e.wmClasses, ":")
	var wmClass = e.wmClasses
	if len(wmClasses) == 2 {
		wmClass = wmClasses[1]
	}

	e.UpdateAuxiliaryText(ibus.NewText("Nhấn (1/2/3/4/5/6/7) để lưu tùy chọn của bạn"), true)

	lt := ibus.NewLookupTable()
	lt.PageSize = uint32(len(imLookupTable))
	lt.Orientation = IBUS_ORIENTATION_VERTICAL
	for im := 1; im <= len(imLookupTable); im++ {
		if e.inputMode == im {
			lt.AppendLabel("*")
			lt.SetCursorPos(uint32(im - 1))
		} else {
			lt.AppendLabel(strconv.Itoa(im))
		}
		if im == usIM {
			lt.AppendCandidate(imLookupTable[im] + " (" + wmClass + ")")
		} else {
			lt.AppendCandidate(imLookupTable[im])
		}
	}
	e.inputModeLookupTable = lt
	e.UpdateLookupTable(lt, true)
}

func (e *IBusBambooEngine) ltProcessKeyEvent(keyVal uint32, keyCode uint32, state uint32) (bool, *dbus.Error) {
	var wmClasses = x11GetFocusWindowClass()
	//e.HideLookupTable()
	fmt.Printf("keyCode 0x%04x keyval 0x%04x | %c\n", keyCode, keyVal, rune(keyVal))
	//e.HideAuxiliaryText()
	if wmClasses == "" {
		return true, nil
	}
	if keyVal == IBUS_OpenLookupTable {
		e.closeInputModeCandidates()
		return false, nil
	}
	var keyRune = rune(keyVal)
	if keyVal == IBUS_Left || keyVal == IBUS_Up {
		e.CursorUp()
		return true, nil
	} else if keyVal == IBUS_Right || keyVal == IBUS_Down {
		e.CursorDown()
		return true, nil
	} else if keyVal == IBUS_Page_Up {
		e.PageUp()
		return true, nil
	} else if keyVal == IBUS_Page_Down {
		e.PageDown()
		return true, nil
	}
	if keyVal == IBUS_Return {
		e.commitInputModeCandidate()
		e.closeInputModeCandidates()
		return true, nil
	}
	if keyRune >= '1' && keyRune <= '9' {
		if pos, err := strconv.Atoi(string(keyRune)); err == nil {
			if e.inputModeLookupTable.SetCursorPos(uint32(pos - 1)) {
				e.commitInputModeCandidate()
				e.closeInputModeCandidates()
				return true, nil
			} else {
				e.closeInputModeCandidates()
			}
		}
	}
	e.closeInputModeCandidates()
	return false, nil
}

func (e *IBusBambooEngine) commitInputModeCandidate() {
	var wmClasses = x11GetFocusWindowClass()
	var im = e.inputModeLookupTable.CursorPos + 1
	e.config.InputModeTable[wmClasses] = int(im)

	saveConfig(e.config, e.engineName)
	e.propList = GetPropListByConfig(e.config)
	e.RegisterProperties(e.propList)
	e.inputMode = e.getInputMode()
}

func (e *IBusBambooEngine) closeInputModeCandidates() {
	e.inputModeLookupTable = nil
	e.UpdateLookupTable(ibus.NewLookupTable(), true) // workaround for issue #18
	e.HidePreeditText()
	e.HideLookupTable()
	e.HideAuxiliaryText()
	e.isInputModeLTOpened = false
}

func (e *IBusBambooEngine) updateInputModeLT() {
	var visible = len(e.inputModeLookupTable.Candidates) > 0
	e.UpdateLookupTable(e.inputModeLookupTable, visible)
}

func (e *IBusBambooEngine) isValidState(state uint32) bool {
	if state&IBUS_CONTROL_MASK != 0 ||
		state&IBUS_MOD1_MASK != 0 ||
		state&IBUS_IGNORED_MASK != 0 ||
		state&IBUS_SUPER_MASK != 0 ||
		state&IBUS_HYPER_MASK != 0 ||
		state&IBUS_META_MASK != 0 {
		return false
	}
	return true
}

func (e *IBusBambooEngine) canProcessKey(keyVal, state uint32) bool {
	if keyVal == IBUS_Space || keyVal == IBUS_BackSpace {
		return true
	}
	return e.preeditor.CanProcessKey(rune(keyVal))
}

func (e *IBusBambooEngine) inBackspaceWhiteList() bool {
	return e.inputMode > preeditIM && e.inputMode < usIM
}

func (e *IBusBambooEngine) inBrowserList() bool {
	return inStringList(DefaultBrowserList, e.wmClasses)
}

func (e *IBusBambooEngine) checkInputMode(im int) bool {
	return e.inputMode == im
}

func notify(enMode bool) {
	var title = "Vietnamese"
	var msg = "Press Shift to switch to English"
	if enMode {
		title = "English"
		msg = "Press Shift to switch to Vietnamese"
	}
	conn, err := dbus.SessionBus()
	if err != nil {
		fmt.Println(err)
		return
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	call := obj.Call("org.freedesktop.Notifications.Notify", 0, "", uint32(281025),
		"", title, msg, []string{}, map[string]dbus.Variant{}, int32(3000))
	if call.Err != nil {
		fmt.Println(call.Err)
	}
}
