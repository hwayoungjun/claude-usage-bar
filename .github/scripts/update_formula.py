#!/usr/bin/env python3
"""Update Homebrew formula version, urls, and sha256 checksums."""
import sys

version, sha_arm64, sha_amd64 = sys.argv[1], sys.argv[2], sys.argv[3]
base = f"https://github.com/hwayoungjun/claude-usage-bar/releases/download/v{version}"

with open("Formula/claude-usage-bar.rb") as f:
    lines = f.readlines()

section, depth, out = None, 0, []
for line in lines:
    s = line.strip()
    if section is None:
        if s == "on_arm do":
            section, depth = "arm", 1
        elif s == "on_intel do":
            section, depth = "intel", 1
        elif s.startswith("version "):
            indent = len(line) - len(line.lstrip())
            line = " " * indent + f'version "{version}"\n'
    else:
        if s.endswith(" do") or s == "do" or s.startswith("def "):
            depth += 1
        elif s == "end":
            depth -= 1
            if depth == 0:
                section = None
        if depth == 1 and s.startswith("url "):
            indent = len(line) - len(line.lstrip())
            fname = "arm64" if section == "arm" else "amd64"
            line = " " * indent + f'url "{base}/claude-usage-bar-darwin-{fname}.tar.gz"\n'
        elif depth == 1 and s.startswith("sha256 "):
            indent = len(line) - len(line.lstrip())
            sha = sha_arm64 if section == "arm" else sha_amd64
            line = " " * indent + f'sha256 "{sha}"\n'
    out.append(line)

with open("Formula/claude-usage-bar.rb", "w") as f:
    f.writelines(out)

print(f"Updated formula to v{version}")
