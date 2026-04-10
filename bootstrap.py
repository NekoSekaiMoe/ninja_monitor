#!/usr/bin/env python3
"""Bootstrap wrapper - builds and executes Go bootstrap binary."""
import os, subprocess, sys
from pathlib import Path

root = Path(__file__).parent.resolve()
build = root / "build"
build.mkdir(exist_ok=True)

bootstrap_bin = build / "bootstrap"
src_mtime = (root / "cmd" / "bootstrap" / "main.go").stat().st_mtime
if not bootstrap_bin.exists() or bootstrap_bin.stat().st_mtime < src_mtime:
    subprocess.run(["go", "build", "-o", str(bootstrap_bin), "./cmd/bootstrap"], cwd=root, check=True)

os.execv(str(bootstrap_bin), [str(bootstrap_bin)] + sys.argv[1:])