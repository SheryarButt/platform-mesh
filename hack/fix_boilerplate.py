#!/usr/bin/env python3

# Copyright The Platform Mesh Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Fix counterpart to verify_boilerplate.py: prepends the reference boilerplate
# header to every file that fails the check.
#
# Files that already carry a *different* copyright notice near the top (e.g.
# code vendored from another project) are never modified — they are reported
# so a human can either fix them by hand or add them to
# hack/boilerplate/skip.txt.

import argparse
import glob
import os
import re
import sys

# How many lines from the top of a file to scan for an existing (foreign)
# copyright notice before deciding it is safe to prepend ours.
FOREIGN_HEADER_SCAN_LINES = 20


def get_args():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "filenames",
        help="list of files to fix, all files if unspecified",
        nargs='*')

    rootdir = os.path.abspath('.')
    parser.add_argument("--rootdir",
                        default=rootdir,
                        help="root directory to examine")

    default_boilerplate_dir = os.path.join(rootdir, "hack/boilerplate")
    parser.add_argument("--boilerplate-dir", default=default_boilerplate_dir)

    parser.add_argument(
        '--skip',
        default=[
            'external/bazel_tools',
            '.git',
            'node_modules',
            '_output',
            'third_party',
            'vendor',
            'verify/boilerplate/test',
            'verify_boilerplate.py',
        ],
        action='append',
        help='Customize paths to avoid',
    )
    return parser.parse_args()


def get_refs():
    # extension (or basename) -> header template text, newline-terminated.
    refs = {}
    template_dir = ARGS.boilerplate_dir
    if not os.path.isdir(template_dir):
        template_dir = os.path.dirname(template_dir)
    for path in glob.glob(os.path.join(template_dir, "boilerplate.*.txt")):
        extension = os.path.basename(path).split(".")[1]
        with open(path, 'r', encoding='utf-8') as ref_file:
            refs[extension] = ref_file.read()
    return refs


def get_regexs():
    regexs = {}
    regexs["date"] = re.compile(r'Copyright ((201[4-9]|202[0-5]) )?')
    regexs["go_build_constraints"] = re.compile(
        r"^(//( \+build|go:build).*\n)+\n", re.MULTILINE)
    regexs["shebang"] = re.compile(r"^(#!.*\n)\n*", re.MULTILINE)
    return regexs


def file_extension(filename):
    return os.path.splitext(filename)[1].split(".")[-1].lower()


# Mirrors verify_boilerplate.py exactly, so fix targets the same file set.
def file_passes(filename, refs, regexs):
    try:
        with open(filename, 'r', encoding='utf-8') as fp:
            file_data = fp.read()
    except IOError:
        return False
    return content_passes(file_data, filename, refs, regexs)


def content_passes(file_data, filename, refs, regexs):
    if not file_data:
        return True

    basename = os.path.basename(filename)
    extension = file_extension(filename)
    ref = refs[extension if extension != "" else basename].splitlines()

    if extension == "go":
        file_data = regexs["go_build_constraints"].sub("", file_data, 1)
    if extension in ("sh", "py"):
        file_data = regexs["shebang"].sub("", file_data, 1)

    data = file_data.splitlines()
    if len(ref) > len(data):
        return False
    data = data[:len(ref)]

    when = regexs["date"]
    for idx, datum in enumerate(data):
        (data[idx], found) = when.subn('Copyright ', datum)
        if found != 0:
            break

    return ref == data


def fix_file(filename, refs, regexs):
    """Returns None on success, or a reason string if the file was left alone."""
    with open(filename, 'r', encoding='utf-8') as fp:
        file_data = fp.read()

    basename = os.path.basename(filename)
    extension = file_extension(filename)
    header = refs[extension if extension != "" else basename]

    # Keep the shebang as the first line; the verifier strips it (and any
    # blank lines after it) before comparing.
    prefix = ''
    body = file_data
    if extension in ("sh", "py"):
        match = regexs["shebang"].match(file_data)
        if match:
            prefix = match.group(1) + "\n"
            body = file_data[match.end():]

    # A different copyright notice near the top means the file was vendored or
    # derived from another project. Stacking our header on top of it is not
    # ours to decide — leave it to a human (fix by hand, or skip.txt).
    top = "\n".join(body.splitlines()[:FOREIGN_HEADER_SCAN_LINES])
    if "Copyright" in top:
        # Exception: kubebuilder scaffolds a header with a bare year and no
        # holder ("Copyright 2025."). If normalizing that one line to our
        # copyright line makes the file pass, the header is otherwise ours —
        # rewrite the line instead of stacking a second header.
        candidate = re.sub(r'Copyright 20\d\d\.',
                           'Copyright The Platform Mesh Authors.',
                           file_data, count=1)
        if candidate != file_data and content_passes(
                candidate, filename, refs, regexs):
            with open(filename, 'w', encoding='utf-8') as fp:
                fp.write(candidate)
            return None
        return "existing copyright notice"

    with open(filename, 'w', encoding='utf-8') as fp:
        fp.write(prefix + header + "\n" + body)
    return None


def normalize_files(files):
    newfiles = []
    for pathname in files:
        if any(x in pathname for x in ARGS.skip):
            continue
        newfiles.append(pathname)
    for idx, pathname in enumerate(newfiles):
        if not os.path.isabs(pathname):
            newfiles[idx] = os.path.join(ARGS.rootdir, pathname)
    return newfiles


def get_files(extensions):
    files = []
    if ARGS.filenames:
        files = ARGS.filenames
    else:
        for root, dirs, walkfiles in os.walk(ARGS.rootdir):
            for dpath in ARGS.skip:
                if dpath in dirs:
                    dirs.remove(dpath)
            for name in walkfiles:
                files.append(os.path.join(root, name))

    files = normalize_files(files)
    outfiles = []
    for pathname in files:
        basename = os.path.basename(pathname)
        extension = file_extension(pathname)
        if extension in extensions or basename in extensions:
            outfiles.append(pathname)
    return outfiles


def main():
    regexs = get_regexs()
    refs = get_refs()
    filenames = get_files(refs.keys())

    fixed = []
    left_alone = []
    for filename in sorted(filenames):
        if file_passes(filename, refs, regexs):
            continue
        reason = fix_file(filename, refs, regexs)
        rel = os.path.relpath(filename, ARGS.rootdir)
        if reason is None:
            fixed.append(rel)
        else:
            left_alone.append((rel, reason))

    for path in fixed:
        print("fixed %s" % path)
    if fixed:
        print("%d files fixed" % len(fixed))

    if left_alone:
        print("\n%d files NOT fixed - resolve by hand or add to "
              "hack/boilerplate/skip.txt:" % len(left_alone))
        for path, reason in left_alone:
            print("  %s (%s)" % (path, reason))
        sys.exit(1)


if __name__ == "__main__":
    ARGS = get_args()
    main()
