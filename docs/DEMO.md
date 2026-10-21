# Demo walkthrough

The committed [demo.mp4](demo.mp4) is a 3 minute 20 second narrated walkthrough with on-screen explanations. It renders a transcript from actual CLI subprocesses; it is not a recording of desktop mouse interaction. The displayed results are checked by the demo script. Audio is not required to follow it.

## Reproduce

```sh
go build -o bin/catalog ./cmd/catalog
python scripts/demo.py --binary ./bin/catalog
python -m pip install Pillow imageio-ffmpeg
python scripts/render_demo.py
```

On Windows build/use `bin/catalog.exe`. Python and the video libraries are demo tooling only; the application and Go tests do not require them. `demo.py` uses a fresh temporary directory and an ephemeral supplier port, checks each expected exit code, and terminates its child supplier on completion. The JSON transcript records actual commands, parsed outputs and exit codes. `render_demo.py` uses Pillow for terminal-style frames and FFmpeg for MP4 encoding. On Windows it adds synthetic English narration using System.Speech; other platforms produce a captioned video. Windows execution policy is scoped to the child PowerShell process; no persistent setting is changed. Use `--no-audio` for captions only.

## Chapters

| Time | Demonstration |
| --- | --- |
| 0:00–0:20 | Product, fictional supplier, and one binary. |
| 0:20–0:40 | Baseline: three products across two batches. |
| 0:40–1:00 | Changed mug, withdrawn lamp, supplier outage. |
| 1:00–1:20 | Existing catalog survives; incomplete status stays blocked. |
| 1:20–1:40 | New CLI process resumes from batch 1; two data issues remain. |
| 1:40–2:00 | Exact before/after audit and rejected record details. |
| 2:00–2:20 | Higher-version corrections resolve outstanding issues. |
| 2:20–2:40 | Full replay: zero changes, no duplicates, lamp stays withdrawn. |
| 2:40–3:00 | Explicit stop after one batch, then resume in a new process. |
| 3:00–3:20 | Recovery boundary, real-kill tests, assumptions and AI use. |

For an interview, you can run the same commands interactively using the README. The generated video introduces the application using synthetic narration; add your own personal introduction if the reviewer prefers one.
