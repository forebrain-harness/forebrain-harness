#!/bin/bash
# magick_ops.sh — ImageMagick wrapper for forebrain image-edit skill
# Usage: magick_ops.sh <operation> [args...]

set -euo pipefail

MAGICK=""
if command -v magick >/dev/null 2>&1; then
    MAGICK="magick"
elif command -v convert >/dev/null 2>&1; then
    MAGICK="convert"
else
    echo "ERROR: ImageMagick not found. Install: brew install imagemagick" >&2
    exit 1
fi

OP="${1:-help}"
shift || true

case "$OP" in
    resize)
        # Usage: magick_ops.sh resize <input> <WxH> <output>
        [ $# -lt 3 ] && { echo "Usage: magick_ops.sh resize <input> <WxH> <output>" >&2; exit 1; }
        $MAGICK "$1" -resize "$2" "$3"
        echo "Resized: $1 → $3 ($2)"
        ;;
    crop)
        # Usage: magick_ops.sh crop <input> <WxH+X+Y> <output>
        [ $# -lt 3 ] && { echo "Usage: magick_ops.sh crop <input> <WxH+X+Y> <output>" >&2; exit 1; }
        $MAGICK "$1" -crop "$2" "$3"
        echo "Cropped: $1 → $3 ($2)"
        ;;
    rotate)
        # Usage: magick_ops.sh rotate <input> <degrees> <output>
        [ $# -lt 3 ] && { echo "Usage: magick_ops.sh rotate <input> <degrees> <output>" >&2; exit 1; }
        $MAGICK "$1" -rotate "$2" "$3"
        echo "Rotated: $1 → $3 (${2}°)"
        ;;
    blur)
        # Usage: magick_ops.sh blur <input> <sigma> <output>
        [ $# -lt 3 ] && { echo "Usage: magick_ops.sh blur <input> <sigma> <output>" >&2; exit 1; }
        $MAGICK "$1" -blur "0x$2" "$3"
        echo "Blurred: $1 → $3 (sigma=$2)"
        ;;
    grayscale)
        # Usage: magick_ops.sh grayscale <input> <output>
        [ $# -lt 2 ] && { echo "Usage: magick_ops.sh grayscale <input> <output>" >&2; exit 1; }
        $MAGICK "$1" -colorspace Gray "$2"
        echo "Grayscale: $1 → $2"
        ;;
    composite)
        # Usage: magick_ops.sh composite <base> <overlay> <gravity> <output>
        [ $# -lt 4 ] && { echo "Usage: magick_ops.sh composite <base> <overlay> <gravity> <output>" >&2; exit 1; }
        $MAGICK "$1" "$2" -gravity "$3" -composite "$4"
        echo "Composite: $1 + $2 → $4 (gravity=$3)"
        ;;
    text)
        # Usage: magick_ops.sh text <input> <text> <gravity> <size> <color> <output>
        [ $# -lt 6 ] && { echo "Usage: magick_ops.sh text <input> <text> <gravity> <size> <color> <output>" >&2; exit 1; }
        $MAGICK "$1" -gravity "$3" -pointsize "$4" -fill "$5" -annotate +0+0 "$2" "$6"
        echo "Text overlay: $1 → $6"
        ;;
    convert)
        # Usage: magick_ops.sh convert <input> <output>
        [ $# -lt 2 ] && { echo "Usage: magick_ops.sh convert <input> <output>" >&2; exit 1; }
        $MAGICK "$1" "$2"
        echo "Converted: $1 → $2"
        ;;
    info)
        # Usage: magick_ops.sh info <input>
        [ $# -lt 1 ] && { echo "Usage: magick_ops.sh info <input>" >&2; exit 1; }
        $MAGICK identify -verbose "$1"
        ;;
    help|*)
        echo "magick_ops.sh — ImageMagick wrapper"
        echo "Operations: resize, crop, rotate, blur, grayscale, composite, text, convert, info"
        echo "Run: magick_ops.sh <op> --help for usage"
        ;;
esac
