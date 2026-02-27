#!/usr/bin/env python3
"""PaddleOCR wrapper — image/PDF to text, stdout output."""
import sys
import json
import os

# Suppress paddle warnings
os.environ["GLOG_minloglevel"] = "3"
os.environ["FLAGS_minloglevel"] = "3"

from paddleocr import PaddleOCR

def ocr_image(image_path, lang="tr", threads=None):
    """Run OCR on a single image and return text lines."""
    if threads:
        os.environ["OMP_NUM_THREADS"] = str(threads)
        os.environ["MKL_NUM_THREADS"] = str(threads)

    ocr = PaddleOCR(
        lang=lang,
        show_log=False,
        use_angle_cls=True,
    )
    result = ocr.predict(image_path)
    lines = []
    for item in result:
        if hasattr(item, 'rec_texts'):
            lines.extend(item.rec_texts)
        elif isinstance(item, dict) and 'rec_texts' in item:
            lines.extend(item['rec_texts'])
        elif isinstance(item, (list, tuple)):
            for sub in item:
                if isinstance(sub, dict) and 'rec_texts' in sub:
                    lines.extend(sub['rec_texts'])
                elif isinstance(sub, (list, tuple)) and len(sub) >= 2:
                    # Legacy format: [[coords, (text, confidence)], ...]
                    text_info = sub[1]
                    if isinstance(text_info, (list, tuple)) and len(text_info) >= 1:
                        lines.append(str(text_info[0]))
                    elif isinstance(text_info, str):
                        lines.append(text_info)
    return "\n".join(lines)

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: paddleocr_wrapper.py <image_path> [lang] [threads]", file=sys.stderr)
        sys.exit(1)

    image_path = sys.argv[1]
    lang = sys.argv[2] if len(sys.argv) > 2 else "tr"
    threads = int(sys.argv[3]) if len(sys.argv) > 3 else None

    # Map common lang codes
    lang_map = {
        "tur": "tr",
        "eng": "en",
        "deu": "de",
        "fra": "fr",
        "spa": "es",
        "ita": "it",
        "por": "pt",
        "rus": "ru",
        "ara": "ar",
        "chi_sim": "ch",
        "jpn": "japan",
        "kor": "korean",
    }
    lang = lang_map.get(lang, lang)

    text = ocr_image(image_path, lang=lang, threads=threads)
    print(text)
