"""Make tiny, synthetic playback fixtures; pass --ffmpeg and --output paths."""
import argparse
from pathlib import Path
import struct
import subprocess


def segment(kind, body=b"", seconds=0):
    return b"PG" + struct.pack(">IIBH", int(seconds * 90000), 0, kind, len(body)) + body


def pgs_fixture():
    def pcs(seconds, count=1, epoch=False):
        body = struct.pack(">HHBHB BBB", 320, 180, 0x10, int(seconds), 0x80 if epoch else 0, 0, 0, count)
        if count:
            body += struct.pack(">HBBHH", 1, 0, 0, 40, 140)
        return segment(0x16, body, seconds)

    # Colored long runs and a fragmented object exercise the actual SUP format.
    rle = bytes([0, 0xC0, 240, 1, 0, 0]) * 12
    head = struct.pack(">HBB", 1, 0, 0x80) + (len(rle) + 4).to_bytes(3, "big") + struct.pack(">HH", 240, 12)
    palette = bytes([0, 0, 0, 16, 128, 128, 0, 1, 235, 128, 128, 255])
    return (pcs(0, epoch=True) + segment(0x14, palette) + segment(0x15, head + rle[:18])
            + segment(0x15, struct.pack(">HBB", 1, 0, 0x40) + rle[18:]) + segment(0x80)
            + pcs(4, count=0) + segment(0x80, seconds=4)
            + pcs(5) + segment(0x80, seconds=5))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--ffmpeg", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    out = Path(args.output).resolve()
    out.mkdir(parents=True, exist_ok=True)
    ass = """[Script Info]
ScriptType: v4.00+
PlayResX: 320
PlayResY: 180
[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1
[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.00,0:00:08.00,Default,,0,0,0,,ASS test {\\c&H00FFFF&}yellow{\\c&HFFFFFF&}
"""
    (out / "test.ass").write_text(ass, encoding="utf-8")
    (out / "test.vtt").write_text("WEBVTT\n\n00:00.000 --> 00:08.000\nVTT test\n", encoding="utf-8")
    (out / "test.sup").write_bytes(pgs_fixture())
    base = [args.ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-filter_threads", "1"]
    subprocess.run(base + ["-f", "lavfi", "-i", "testsrc2=size=320x180:rate=15", "-f", "lavfi", "-i",
        "sine=frequency=440:sample_rate=48000", "-t", "8", "-c:v", "libx264", "-threads", "1",
        "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "+faststart",
        str(out / "native.mp4")], check=True)
    subprocess.run(base + ["-i", str(out / "native.mp4"), "-i", str(out / "test.ass"), "-map", "0", "-map", "1",
        "-c:v", "libx265", "-threads", "1", "-preset", "ultrafast", "-x265-params",
        "pools=none:frame-threads=1:log-level=error", "-c:a", "copy", "-c:s", "ass",
        str(out / "hevc-ass.mkv")], check=True)
    subprocess.run(base + ["-i", str(out / "native.mp4"), "-c", "copy", "-hls_time", "2", "-hls_list_size", "0",
        "-hls_segment_type", "fmp4", str(out / "master.m3u8")], check=True, cwd=out)
    print(f"Synthetic fixtures: {out}")


if __name__ == "__main__":
    main()
