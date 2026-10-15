package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Screencast recording: the browser pushes a JPEG frame whenever the page
// changes (Page.startScreencast); frames are written to disk with their
// arrival time and turned into a real-time MP4 with ffmpeg on stop.
// While a tab records, clicks and typing are briefly highlighted so the
// video shows what the agent did.

type recorder struct {
	mu      sync.Mutex
	dir     string
	frames  []recFrame
	started time.Time
	stopped bool
	out     string // requested output path ("" = state dir)
}

type recFrame struct {
	file string
	at   time.Time
}

func (b *Bridge) recorderFor(tabID string) *recorder {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.recorders[tabID]
}

// ── POST /screencast/start ─────────────────────────────────

func (b *Bridge) handleScreencastStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID     string `json:"tabId"`
		MaxWidth  int64  `json:"maxWidth"`
		MaxHeight int64  `json:"maxHeight"`
		Quality   int64  `json:"quality"`
		Path      string `json:"path"` // output .mp4 (default ~/.seebro/videos/…)
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	ctx, tabID, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	if b.recorderFor(tabID) != nil {
		jsonErr(w, 409, fmt.Errorf("tab %s is already recording", tabID))
		return
	}
	out, err := outputPath(req.Path, ".mp4")
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	if req.MaxWidth <= 0 {
		req.MaxWidth = 1280
	}
	if req.MaxHeight <= 0 {
		req.MaxHeight = 800
	}
	if req.Quality <= 0 || req.Quality > 100 {
		req.Quality = 70
	}

	dir := filepath.Join(stateDir, "screencasts", tabID+"-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		jsonErr(w, 500, err)
		return
	}
	rec := &recorder{dir: dir, started: time.Now(), out: out}

	// Frames arrive as events; each one must be acknowledged before the next
	// is sent. Event callbacks must not block, so ack from a goroutine.
	listenCtx, stopListen := context.WithCancel(ctx)
	chromedp.ListenTarget(listenCtx, func(ev any) {
		e, ok := ev.(*page.EventScreencastFrame)
		if !ok {
			return
		}
		rec.mu.Lock()
		if rec.stopped {
			rec.mu.Unlock()
			return
		}
		n := len(rec.frames) + 1
		rec.mu.Unlock()
		go func() {
			if data, err := base64.StdEncoding.DecodeString(e.Data); err == nil {
				name := filepath.Join(dir, fmt.Sprintf("frame-%06d.jpg", n))
				if os.WriteFile(name, data, 0644) == nil {
					rec.mu.Lock()
					rec.frames = append(rec.frames, recFrame{file: name, at: time.Now()})
					rec.mu.Unlock()
				}
			}
			c := chromedp.FromContext(ctx)
			if c != nil && c.Target != nil {
				_ = page.ScreencastFrameAck(e.SessionID).Do(cdp.WithExecutor(ctx, c.Target))
			}
		}()
	})

	err = chromedp.Run(ctx, page.StartScreencast().
		WithFormat(page.ScreencastFormatJpeg).WithQuality(req.Quality).
		WithMaxWidth(req.MaxWidth).WithMaxHeight(req.MaxHeight).WithEveryNthFrame(1))
	if err != nil {
		stopListen()
		jsonErr(w, 500, fmt.Errorf("start screencast: %w", err))
		return
	}
	b.mu.Lock()
	if b.recorders == nil {
		b.recorders = make(map[string]*recorder)
	}
	b.recorders[tabID] = rec
	b.recStop[tabID] = stopListen
	b.mu.Unlock()
	jsonResp(w, 200, map[string]any{"recording": true, "tabId": tabID, "frames": dir})
}

// ── POST /screencast/stop ──────────────────────────────────

func (b *Bridge) handleScreencastStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID string `json:"tabId"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req)
	ctx, tabID, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	rec := b.recorderFor(tabID)
	if rec == nil {
		jsonErr(w, 409, fmt.Errorf("tab %s is not recording", tabID))
		return
	}
	tCtx, cancel := context.WithTimeout(ctx, actionTimeout)
	_ = chromedp.Run(tCtx, page.StopScreencast())
	cancel()
	time.Sleep(200 * time.Millisecond) // let in-flight frames land on disk

	b.mu.Lock()
	if stop := b.recStop[tabID]; stop != nil {
		stop()
	}
	delete(b.recorders, tabID)
	delete(b.recStop, tabID)
	b.mu.Unlock()

	rec.mu.Lock()
	rec.stopped = true
	frames := append([]recFrame(nil), rec.frames...)
	rec.mu.Unlock()
	end := time.Now()

	res := map[string]any{"frames": len(frames), "seconds": end.Sub(rec.started).Seconds(), "framesDir": rec.dir}
	if len(frames) == 0 {
		jsonErr(w, 500, fmt.Errorf("no frames were captured (headless Chrome only sends frames while the page changes)"))
		return
	}
	path, err := encodeVideo(rec, frames, end)
	if err != nil {
		res["error"] = err.Error()
		res["hint"] = "install ffmpeg to get an MP4; the JPEG frames are kept in framesDir"
		jsonResp(w, 200, res)
		return
	}
	if fi, err := os.Stat(path); err == nil {
		res["size"] = fi.Size()
	}
	res["path"] = path
	_ = os.RemoveAll(rec.dir) // frames are in the video now
	delete(res, "framesDir")
	jsonResp(w, 200, res)
}

// encodeVideo writes an ffmpeg concat list with each frame shown until the
// next one arrived (the last until stop), so the video plays in real time.
func encodeVideo(rec *recorder, frames []recFrame, end time.Time) (string, error) {
	ffmpeg := envOr("FFMPEG_BINARY", "ffmpeg")
	if _, err := exec.LookPath(ffmpeg); err != nil {
		return "", fmt.Errorf("ffmpeg not found")
	}
	var list strings.Builder
	for i, f := range frames {
		next := end
		if i+1 < len(frames) {
			next = frames[i+1].at
		}
		d := next.Sub(f.at).Seconds()
		if d < 0.01 {
			d = 0.01
		}
		fmt.Fprintf(&list, "file '%s'\nduration %.3f\n", f.file, d)
	}
	fmt.Fprintf(&list, "file '%s'\n", frames[len(frames)-1].file) // concat demuxer needs the last file twice
	listPath := filepath.Join(rec.dir, "frames.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0644); err != nil {
		return "", err
	}
	out := rec.out
	if out == "" {
		out = filepath.Join(stateDir, "videos", "seebro-"+rec.started.Format("20060102-150405")+".mp4")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", err
	}
	// -t trims the extra frame the concat demuxer adds for the repeated last entry.
	total := end.Sub(frames[0].at).Seconds()
	input := []string{"-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", listPath,
		"-vf", "fps=25,scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p", "-t", fmt.Sprintf("%.3f", total)}
	codecs := [][]string{
		{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23"},
		{"-c:v", "mpeg4", "-q:v", "4"}, // ffmpeg builds without libx264
	}
	var errs []string
	for _, c := range codecs {
		args := append(append(append([]string{}, input...), c...), "-movflags", "+faststart", out)
		msg, err := exec.Command(ffmpeg, args...).CombinedOutput()
		if err == nil {
			slog.Info("screencast saved", "path", out, "frames", len(frames))
			return out, nil
		}
		errs = append(errs, fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(msg))))
	}
	return "", fmt.Errorf("ffmpeg: %s", strings.Join(errs, " / "))
}

// highlightJS draws a short-lived marker around an element (only while a
// tab is being recorded) so the video shows what the agent interacted with.
const highlightJS = `function(label) {
	const r = this.getBoundingClientRect();
	const d = document.createElement("div");
	d.setAttribute("data-seebro-highlight", "");
	d.style.cssText = "position:fixed;z-index:2147483647;pointer-events:none;border:3px solid #e11d48;border-radius:6px;" +
		"box-shadow:0 0 0 4px rgba(225,29,72,.25);transition:opacity .4s;left:" + (r.x - 4) + "px;top:" + (r.y - 4) + "px;width:" +
		(r.width + 2) + "px;height:" + (r.height + 2) + "px";
	if (label) {
		const t = document.createElement("div");
		t.textContent = label;
		t.style.cssText = "position:absolute;top:-22px;left:-3px;background:#e11d48;color:#fff;font:12px/18px sans-serif;padding:0 6px;border-radius:4px;white-space:nowrap";
		d.appendChild(t);
	}
	(document.body || document.documentElement).appendChild(d);
	setTimeout(() => { d.style.opacity = "0"; }, 700);
	setTimeout(() => d.remove(), 1200);
}`

// highlightForRecording marks the action's element when the tab records.
func (b *Bridge) highlightForRecording(ctx context.Context, tabID string, req actionRequest) {
	if b.recorderFor(tabID) == nil || (req.NodeID == 0 && req.Selector == "") {
		return
	}
	label := req.Kind
	if req.Text != "" && (req.Kind == actionType || req.Kind == actionFill || req.Kind == actionHumanType) {
		label += ": " + truncate(req.Text, 30)
	}
	if _, err := callOnTarget(ctx, req.NodeID, req.Selector, highlightJS, label); err == nil {
		time.Sleep(350 * time.Millisecond) // give the frame time to show the marker before the page changes
	}
}
