package main

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// TCP bot wire protocol helpers — see
// https://github.com/freehuntx/gpn-tron/blob/master/PROTOCOL.md
//
// formatPacket/writePacket are the cold-path helpers, used for
// join/error/win/lose — never per-tick. fmt is fine here and reads more
// clearly. writePacket writes directly (pre-join, before a botSink
// exists); formatPacket feeds Player.send, which enqueues on the sink.
//
// appendPos and appendPlayer are the hot-path helpers, called once per
// alive player per tick when building the broadcast frame. They use
// strconv.AppendInt + direct []byte appends to stay alloc-free; the per-tick
// broadcast is O(players), so on large games fmt's reflection allocs would
// dominate frame-build time. If you change the wire format, update both
// styles and the matching reader in bot tests.

func formatPacket(parts ...any) []byte {
	vals := make([]string, len(parts))
	for i, part := range parts {
		vals[i] = fmt.Sprint(part)
	}
	return []byte(strings.Join(vals, "|") + "\n")
}

func writePacket(w *bufio.Writer, parts ...any) {
	if w == nil {
		return
	}
	w.Write(formatPacket(parts...))
	w.Flush()
}

func appendPos(buf []byte, id, x, y int) []byte {
	buf = append(buf, "pos|"...)
	buf = strconv.AppendInt(buf, int64(id), 10)
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, int64(x), 10)
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, int64(y), 10)
	return append(buf, '\n')
}

func appendPlayer(buf []byte, id int, name string) []byte {
	buf = append(buf, "player|"...)
	buf = strconv.AppendInt(buf, int64(id), 10)
	buf = append(buf, '|')
	buf = append(buf, name...)
	return append(buf, '\n')
}
