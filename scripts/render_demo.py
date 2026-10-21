#!/usr/bin/env python3
"""Render the verified CLI transcript as a 3m20s narrated/captioned MP4."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import textwrap
import wave

from PIL import Image, ImageDraw, ImageFont
import imageio_ffmpeg

ROOT = Path(__file__).resolve().parents[1]
WIDTH, HEIGHT, FPS = 1280, 720, 2


def font(size, mono=False):
    candidates = (["C:/Windows/Fonts/consola.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"] if mono else ["C:/Windows/Fonts/segoeui.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"])
    for path in candidates:
        if Path(path).exists():
            return ImageFont.truetype(path, size)
    return ImageFont.load_default(size=size)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--no-audio", action="store_true", help="render captions only")
    parser.add_argument("--reuse-video", action="store_true", help="reuse the already rendered silent video after an audio-only failure")
    args = parser.parse_args()
    transcript = ROOT / "docs/demo-transcript.json"
    scenes = json.loads(transcript.read_text(encoding="utf-8"))["scenes"]
    work = ROOT / ".demo-work/video"
    work.mkdir(parents=True, exist_ok=True)
    ffmpeg = imageio_ffmpeg.get_ffmpeg_exe()
    title_font, caption_font = font(34), font(21)
    mono_font, small_font = font(19, True), font(17)
    total = sum(scene["seconds"] for scene in scenes)
    silent = work / "silent.mp4"
    if args.reuse_video and not silent.is_file():
        parser.error("No rendered silent video to reuse")
    encoder = None if args.reuse_video else subprocess.Popen([ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-vcodec", "rawvideo", "-s", f"{WIDTH}x{HEIGHT}", "-pix_fmt", "rgb24", "-r", str(FPS), "-i", "-", "-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p", str(silent)], stdin=subprocess.PIPE)
    elapsed = 0
    for number, scene in enumerate(scenes):
        print(f"Scene {number + 1}/{len(scenes)}: {scene['title']}", flush=True)
        canvas = Image.new("RGB", (WIDTH, HEIGHT), "#0b1323")
        draw = ImageDraw.Draw(canvas)
        draw.text((52, 28), "NORTHSTAR  /  SUPPLIER CATALOG", font=small_font, fill="#6ee7c0")
        draw.text((52, 64), scene["title"], font=title_font, fill="#f1f5f9")
        for row, line in enumerate(textwrap.wrap(scene["caption"], width=106)):
            draw.text((54, 114 + row * 25), line, font=caption_font, fill="#acbad1")
        draw.rounded_rectangle((50, 168, 1230, 600), radius=14, fill="#111e33", outline="#273b58", width=2)
        for i, color in enumerate(["#fc6d68", "#f6c352", "#59c68e"]):
            draw.ellipse((70 + i * 22, 186, 80 + i * 22, 196), fill=color)
        draw.text((158, 181), "catalog terminal  |  verified command output, compact view", font=small_font, fill="#7c91ae")
        y = 217
        for line in scene["lines"]:
            for wrapped in textwrap.wrap(line, width=97, replace_whitespace=False) or [""]:
                if y > 578:
                    raise ValueError(f"Too many terminal lines in scene {number}: {scene['title']}")
                draw.text((72, y), wrapped, font=mono_font, fill="#75e4bd" if wrapped.startswith("$") else "#d3ddec")
                y += 24
        draw.text((52, 620), "Fictional supplier data  |  Go + bbolt  |  github.com/Belldenchoi/supplier-catalog-go", font=small_font, fill="#8da3bf")
        for frame in range(int(scene["seconds"] * FPS)):
            current = elapsed + frame / FPS
            image = canvas.copy()
            progress = ImageDraw.Draw(image)
            progress.rounded_rectangle((52, 661, 1228, 666), radius=2, fill="#263650")
            progress.rounded_rectangle((52, 661, 52 + int(1176 * current / total), 666), radius=2, fill="#64dfb4")
            label = f'{int(current)//60}:{int(current)%60:02d} / {int(total)//60}:{int(total)%60:02d}    {number+1:02d}/{len(scenes):02d}'
            progress.text((1045, 681), label, font=small_font, fill="#8da3bf")
            if encoder is not None:
                encoder.stdin.write(image.tobytes())
        canvas.save(work / f"scene-{number}.png")
        elapsed += scene["seconds"]
    if encoder is not None:
        encoder.stdin.close()
        if encoder.wait() != 0:
            raise RuntimeError("FFmpeg video encoding failed")

    output = ROOT / "docs/demo.mp4"
    if not args.no_audio and os.name == "nt":
        subprocess.run(["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", str(ROOT / "scripts/narrate.ps1"), "-Transcript", str(transcript), "-OutputDirectory", str(work)], check=True)
        # Pad each spoken chapter with silence to exactly its video duration.
        audio_parts = []
        for number, scene in enumerate(scenes):
            wav = work / f"scene-{number}.wav"
            with wave.open(str(wav), "rb") as source:
                duration = source.getnframes() / source.getframerate()
            speed = max(1.0, duration / (scene["seconds"] - 0.5))
            if speed > 2:
                raise ValueError(f"Scene {number} narration is too long for its chapter")
            padded = work / f"padded-{number}.wav"
            subprocess.run([ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", str(wav), "-af", f"atempo={speed:.6f},apad", "-t", str(scene["seconds"]), "-ar", "44100", "-ac", "1", str(padded)], check=True)
            audio_parts.append(padded)
        concat = work / "audio.txt"
        concat.write_text("\n".join(f"file '{p.as_posix()}'" for p in audio_parts), encoding="utf-8")
        narration = work / "narration.wav"
        subprocess.run([ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "concat", "-safe", "0", "-i", str(concat), "-c", "copy", str(narration)], check=True)
        subprocess.run([ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", str(silent), "-i", str(narration), "-c:v", "copy", "-c:a", "aac", "-b:a", "96k", "-t", str(total), "-movflags", "+faststart", str(output)], check=True)
    else:
        subprocess.run([ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", str(silent), "-c", "copy", "-movflags", "+faststart", str(output)], check=True)
    print(f"Created {output} ({total}s, {output.stat().st_size / 1024 / 1024:.1f} MiB)")


if __name__ == "__main__":
    main()
