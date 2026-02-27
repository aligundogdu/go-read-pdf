#!/usr/bin/env python3
"""OCR wrapper — image to text via EasyOCR, stdout output."""
import sys
import os
import logging
import warnings

logging.disable(logging.WARNING)
warnings.filterwarnings("ignore")

import easyocr


def ocr_image(image_path, lang="tr", threads=None):
    """Run OCR on a single image and return text lines."""
    if threads:
        import torch
        torch.set_num_threads(int(threads))

    # EasyOCR lang codes: Turkish="tr", English="en"
    langs = [lang]
    if lang == "tr":
        langs = ["tr", "en"]  # Türkçe + Latin karakterler için en de ekle

    reader = easyocr.Reader(langs, gpu=False, verbose=False)
    results = reader.readtext(image_path)

    lines = [text for (_, text, conf) in results]
    return "\n".join(lines)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: paddleocr_wrapper.py <image_path> [lang] [threads]", file=sys.stderr)
        sys.exit(1)

    image_path = sys.argv[1]
    lang = sys.argv[2] if len(sys.argv) > 2 else "tr"
    threads = int(sys.argv[3]) if len(sys.argv) > 3 else None

    # Map tesseract lang codes to EasyOCR codes
    lang_map = {
        "tur": "tr",
        "eng": "en",
        "tur+eng": "tr",
        "eng+tur": "en",
        "deu": "de",
        "fra": "fr",
        "spa": "es",
        "ita": "it",
        "por": "pt",
        "rus": "ru",
        "ara": "ar",
        "chi_sim": "ch_sim",
        "jpn": "ja",
        "kor": "ko",
    }
    lang = lang_map.get(lang, lang)

    text = ocr_image(image_path, lang=lang, threads=threads)
    print(text)
