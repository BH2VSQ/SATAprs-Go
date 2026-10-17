#!/usr/bin/env python3
"""Restore missing SatAPRS-Go packet formatting helpers in app.go only."""
from __future__ import annotations

import datetime as dt
from pathlib import Path

ROOT = Path(__file__).resolve().parent
APP = ROOT / "app.go"

TNC2_HELPER = '''// formatTNC2Packet renders an AX.25 UI frame as the conventional APRS/TNC2
// monitor line: SOURCE>DESTINATION,PATH:INFO.
func formatTNC2Packet(packet aprs.Packet) string {
\tline := packet.Source + ">" + packet.Destination
\tif len(packet.Path) > 0 {
\t\tline += "," + strings.Join(packet.Path, ",")
\t}
\treturn line + ":" + packet.Info
}

'''

UISS_HELPER = '''// formatUISSFrame renders the decoded AX.25 UI frame in the two-line monitor
// style used by UISS. The information length is measured in bytes, as on air.
func formatUISSFrame(packet aprs.Packet, at time.Time) string {
\theader := fmt.Sprintf("Fm %s To %s", packet.Source, packet.Destination)
\tif len(packet.Path) > 0 {
\t\theader += " Via " + strings.Join(packet.Path, ",")
\t}
\theader += fmt.Sprintf(" <UI pid=F0 Len=%d >[%s]", len([]byte(packet.Info)), at.Local().Format("15:04:05"))
\treturn header + "\\n" + packet.Info
}

'''


def main() -> int:
    if not APP.is_file():
        print("未找到 app.go。请将本脚本放在 SATAprs-Go 仓库根目录。")
        return 2

    raw = APP.read_bytes()
    bom = raw.startswith(b"\xef\xbb\xbf")
    newline = "\r\n" if b"\r\n" in raw else "\n"
    text = raw.decode("utf-8-sig").replace("\r\n", "\n")

    missing = []
    if "func formatTNC2Packet(" not in text:
        missing.append(TNC2_HELPER)
    if "func formatUISSFrame(" not in text:
        missing.append(UISS_HELPER)
    if not missing:
        print("formatTNC2Packet 和 formatUISSFrame 均已存在，未修改任何文件。")
        return 0

    anchor = "func normalizeFrameDestination(value string) (string, error) {"
    if text.count(anchor) != 1:
        print("修复失败：normalizeFrameDestination 插入锚点不唯一，未写入文件。")
        return 1

    patched = text.replace(anchor, "".join(missing) + anchor, 1)
    for name in ("formatTNC2Packet", "formatUISSFrame"):
        if patched.count(f"func {name}(") != 1:
            print(f"修复失败：{name} 定义检查未通过，未写入文件。")
            return 1

    stamp = dt.datetime.now().strftime("%Y%m%d_%H%M%S")
    backup = APP.with_name(f"app.go.before_format_helpers_{stamp}.bak")
    backup.write_bytes(raw)
    output = patched.replace("\n", newline).encode("utf-8")
    if bom:
        output = b"\xef\xbb\xbf" + output
    APP.write_bytes(output)
    print("已恢复缺失的消息格式化辅助函数：" + ", ".join(
        name for name in ("formatTNC2Packet", "formatUISSFrame")
        if name in ["formatTNC2Packet" if TNC2_HELPER in missing else "", "formatUISSFrame" if UISS_HELPER in missing else ""]
    ))
    print(f"已备份原文件：{backup.name}")
    print("没有修改 AFSK 解码、音频采集、PTT 或发射音调逻辑。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
