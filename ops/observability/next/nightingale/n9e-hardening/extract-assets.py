#!/usr/bin/env python3
"""Recover pinned Nightingale assets without executing the ELF or using a network.

Only Linux amd64 ET_EXEC files with no relocation sections are supported. Go
1.18+ inline build information and Go's pointer/length/capacity embed layout are
decoded directly. Archive bodies are validated in bounded streams, never unpacked.
The destination must not exist. extraction.json is written last as a completion
record; a failed run may leave an incomplete destination, which must not be used.
"""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import struct
import sys
import zipfile
import zlib


SOURCE_COMMIT = "4e056150142b2e1ed6966c9317a9e08442c399f7"
MODULE = "github.com/ccfos/nightingale/v6"
REDIS_MODULE = "github.com/redis/go-redis/v9"
CHUNK = 1 << 20
MAX_ASSET = 256 << 20
MAX_INFLATED = 1 << 30
MAX_MEMBERS = 60000
MAX_METADATA = 1 << 20
BUILD_MAGIC = b"\xff Go buildinf:"
SANDBOX = "pkg/sandbox/embedassets/linux_amd64/"
CORPUS = "aiagent/skill/embedded/codeassets/"
FRONTEND = "front/statik/frontend.zip"
PATHS = {
    "bwrap": SANDBOX + "bwrap",
    "python": SANDBOX + "python-base.tar.gz",
    "init": SANDBOX + "n9e-sandbox-init",
    "n9e": CORPUS + "n9e.tar.gz",
    "categraf": CORPUS + "categraf.tar.gz",
    "fe": CORPUS + "fe.tar.gz",
    "manifest": CORPUS + "manifest.json",
    "frontend": FRONTEND,
}


class Invalid(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise Invalid(message)


class Source:
    def __init__(self, path):
        self.fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        self.initial = os.fstat(self.fd)
        require(stat.S_ISREG(self.initial.st_mode), "binary must be a regular file")
        self.size = self.initial.st_size
        require(64 <= self.size <= 2 << 30, "binary size outside supported bounds")

    def read(self, offset, size):
        require(0 <= offset <= self.size and 0 <= size <= self.size - offset,
                "read outside binary")
        data = os.pread(self.fd, size, offset)
        require(len(data) == size, "short read; binary may have changed")
        return data

    def digest(self, offset, size):
        result = hashlib.sha256()
        for pos in range(offset, offset + size, CHUNK):
            result.update(self.read(pos, min(CHUNK, offset + size - pos)))
        return result.hexdigest()

    def unchanged(self):
        current = os.fstat(self.fd)
        fields = ("st_dev", "st_ino", "st_size", "st_mtime_ns", "st_ctime_ns")
        require(all(getattr(current, key) == getattr(self.initial, key) for key in fields),
                "binary changed during extraction")


class Region(io.RawIOBase):
    def __init__(self, source, offset, size):
        self.source, self.offset, self.size, self.pos = source, offset, size, 0

    def readable(self):
        return True

    def seekable(self):
        return True

    def tell(self):
        return self.pos

    def seek(self, offset, whence=os.SEEK_SET):
        target = offset + (0 if whence == os.SEEK_SET else
                           self.pos if whence == os.SEEK_CUR else self.size)
        require(whence in (0, 1, 2) and 0 <= target <= self.size, "invalid region seek")
        self.pos = target
        return target

    def read(self, size=-1):
        size = self.size - self.pos if size < 0 else min(size, self.size - self.pos)
        require(size <= 16 << 20, "oversized buffered read")
        result = self.source.read(self.offset + self.pos, size)
        self.pos += size
        return result


class Elf:
    def __init__(self, source, offset=0, size=None):
        self.source, self.offset = source, offset
        self.size = source.size if size is None else size
        header = struct.unpack("<16sHHIQQQIHHHHHH", self.read(0, 64))
        ident, kind, machine, version = header[:4]
        require(ident[:7] == b"\x7fELF\x02\x01\x01", "expected little-endian ELF64")
        require(kind == 2 and machine == 62 and version == 1,
                "only amd64 ET_EXEC is supported; PIE is rejected")
        phoff, shoff = header[5:7]
        ehsize, phsize, phnum, shsize, shnum, shstrndx = header[8:]
        require(ehsize == 64 and phsize == 56 and 0 < phnum <= 128,
                "invalid ELF program headers")
        require(shsize == 64 and 0 < shnum <= 65534 and shstrndx < shnum,
                "section headers are required; extended numbering is unsupported")
        self.loads, self.sections = [], []
        for index in range(phnum):
            p = struct.unpack("<IIQQQQQQ", self.read(phoff + index * phsize, phsize))
            ptype, flags, poff, va, _, filesz, memsz, align = p
            require(ptype not in (2, 3), "dynamic/interpreted ELF is unsupported")
            if ptype == 1:
                require(filesz <= memsz and poff + filesz <= self.size,
                        f"invalid PT_LOAD bounds: ELF offset={offset} size={self.size} "
                        f"segment={index} file_offset={poff} filesz={filesz} memsz={memsz}")
                require(align in (0, 1) or (align & (align - 1) == 0 and
                        poff % align == va % align), "invalid PT_LOAD alignment")
                self.loads.append({"offset": poff, "va": va, "size": filesz, "flags": flags})
        require(self.loads, "ELF has no loadable segments")
        for index in range(shnum):
            s = struct.unpack("<IIQQQQIIQQ", self.read(shoff + index * shsize, shsize))
            require(not (s[1] in (4, 9, 19) and s[5]), "ELF relocation sections are rejected")
            if s[1] != 8:
                require(s[4] + s[5] <= self.size, "section outside ELF")
            self.sections.append(s)
        names = self.sections[shstrndx]
        require(names[5] <= MAX_METADATA, "oversized ELF section name table")
        strings = self.read(names[4], names[5])
        self.named = {}
        for section in self.sections:
            start = section[0]
            end = strings.find(b"\0", start)
            require(0 <= start < len(strings) and end >= start, "invalid section name")
            name = strings[start:end].decode("ascii")
            require(not name or name not in self.named, "duplicate ELF section name")
            self.named[name] = section

    def read(self, offset, size):
        require(0 <= offset <= self.size and 0 <= size <= self.size - offset,
                "read outside ELF")
        return self.source.read(self.offset + offset, size)

    def mapped(self, address, size):
        matches = [p["offset"] + address - p["va"] for p in self.loads
                   if p["va"] <= address and 0 <= size <= p["va"] + p["size"] - address]
        require(len(matches) == 1, "asset pointer is unmapped or ambiguously mapped")
        return self.offset + matches[0]

    def buildinfo(self):
        section = self.named.get(".go.buildinfo")
        require(section is not None and 32 <= section[5] <= MAX_METADATA,
                "missing or oversized Go build information section")
        blob = self.read(section[4], section[5])
        require(blob[:14] == BUILD_MAGIC and blob[14:16] == b"\x08\x02",
                "unsupported Go build information encoding")
        go_version, pos = inline_string(blob, 32)
        framed, _ = inline_string(blob, pos, decode=False)
        require(len(framed) >= 33 and framed[-17:-16] == b"\n", "invalid module framing")
        raw = framed[16:-16].decode("utf-8")
        result = {"go_version": go_version, "settings": {}, "dependencies": {}}
        last_dependency = None
        for line in raw.splitlines():
            fields = line.split("\t")
            if fields[0] == "path" and len(fields) == 2:
                require("path" not in result, "duplicate Go main path")
                result["path"] = fields[1]
            elif fields[0] in ("dep", "mod") and len(fields) >= 3:
                entry = {"path": fields[1], "version": fields[2],
                         "sum": fields[3] if len(fields) > 3 else ""}
                if fields[0] == "mod":
                    require("main" not in result, "duplicate Go main module")
                    result["main"] = entry
                    last_dependency = entry
                else:
                    require(fields[1] not in result["dependencies"], "duplicate dependency")
                    result["dependencies"][fields[1]] = entry
                    last_dependency = entry
            elif fields[0] == "=>":
                require(last_dependency is not None, "orphan Go module replacement")
                last_dependency["replacement"] = fields[1:]
            elif fields[0] == "build" and len(fields) == 2:
                key, separator, value = fields[1].partition("=")
                require(separator and key not in result["settings"], "invalid build setting")
                result["settings"][key] = value
        result["raw_sha256"] = hashlib.sha256(raw.encode()).hexdigest()
        return result


def inline_string(blob, pos, decode=True):
    size = 0
    for shift in range(0, 70, 7):
        require(pos < len(blob), "truncated build information varint")
        value = blob[pos]
        pos += 1
        size |= (value & 127) << shift
        if value < 128:
            require(size <= MAX_METADATA and pos + size <= len(blob), "invalid build string")
            result = blob[pos:pos + size]
            return (result.decode("utf-8") if decode else result), pos + size
    raise Invalid("oversized build information varint")


def validate_buildinfo(info):
    require(info.get("path") == MODULE + "/cmd/center", "not the Nightingale center binary")
    require(info.get("main", {}).get("path") == MODULE, "wrong Nightingale module")
    settings = info["settings"]
    for key, expected in (("GOARCH", "amd64"), ("GOOS", "linux"), ("CGO_ENABLED", "0"),
                          ("vcs.revision", SOURCE_COMMIT)):
        require(settings.get(key) == expected, "unexpected build setting: " + key)
    require(set(settings.get("-tags", "").strip('"').split(",")) ==
            {"sandbox_embed", "qa_code_embed"}, "both official asset build tags are required")
    redis = info["dependencies"].get(REDIS_MODULE)
    require(redis and redis["version"] == "v9.0.2" and "replacement" not in redis,
            "expected unreplaced official go-redis/v9 v9.0.2")
    require("replacement" not in info["main"], "replaced Nightingale module")
    return {"go_version": info["go_version"], "path": info["path"], "main": info["main"],
            "go_redis": redis, "source_commit": settings["vcs.revision"],
            "vcs_modified": settings.get("vcs.modified"),
            "settings": {k: settings[k] for k in ("GOARCH", "GOOS", "CGO_ENABLED", "-tags",
                         "-buildmode", "vcs.revision", "vcs.modified", "vcs.time") if k in settings},
            "raw_sha256": info["raw_sha256"]}


def slice_candidates(elf):
    found = {}
    for segment in elf.loads:
        if not segment["flags"] & 2:
            continue
        start = segment["offset"] + (-segment["va"] % 8)
        end = segment["offset"] + segment["size"]
        for base in range(start, max(start, end - 23), CHUNK):
            blob = elf.read(base, min(CHUNK + 16, end - base))
            for relative in range(0, min(CHUNK, len(blob) - 23), 8):
                pointer, size, capacity = struct.unpack_from("<QQQ", blob, relative)
                if not (4 <= size == capacity <= MAX_ASSET):
                    continue
                try:
                    offset = elf.mapped(pointer, size)
                except Invalid:
                    continue
                prefix = elf.source.read(offset, min(128, size))
                kind = ("gzip" if prefix.startswith(b"\x1f\x8b\x08") else
                        "elf" if prefix.startswith(b"\x7fELF") else
                        "manifest" if prefix.startswith(b"[\n") and b'"repo"' in prefix
                        and b'"n9e"' in prefix and size <= MAX_METADATA else None)
                # Older protobuf packages also embed gzip-compressed []byte
                # descriptors. Only a checksummed first TAR header is relevant.
                if kind == "gzip" and not looks_like_tar(elf.source, offset, size):
                    continue
                if kind == "elf":
                    # The main ELF header can also be referenced by a small
                    # slice-shaped runtime object. A candidate must contain a
                    # complete embedded ELF, just as gzip candidates must have
                    # a complete TAR header. Missing actual assets still fail
                    # the exact eight-resource set check below.
                    try:
                        Elf(elf.source, offset, size)
                    except Invalid:
                        continue
                if kind:
                    key = (offset, size)
                    entry = found.setdefault(key, {"kind": kind, "offset": offset, "size": size,
                                                   "slice_header_offsets": []})
                    entry["slice_header_offsets"].append(elf.offset + base + relative)
                    require(len(found) <= 64, "too many embedded asset candidates")
    return list(found.values())


class Budget:
    def __init__(self):
        self.used = 0

    def add(self, size):
        self.used += size
        require(self.used <= MAX_INFLATED, "aggregate inflated data exceeds 1 GiB")


class GzipStream:
    def __init__(self, region, budget):
        self.region, self.budget = region, budget
        self.decoder = zlib.decompressobj(31)
        self.pending = b""
        self.finished = False

    def read(self, size):
        require(0 < size <= CHUNK, "invalid gzip read size")
        result = bytearray()
        while len(result) < size and not self.finished:
            data = self.pending or self.region.read(min(65536, self.region.size - self.region.pos))
            require(data, "truncated gzip stream")
            chunk = self.decoder.decompress(data, size - len(result))
            self.budget.add(len(chunk))
            result.extend(chunk)
            self.pending = self.decoder.unconsumed_tail
            if self.decoder.eof:
                require(not self.decoder.unused_data and self.region.pos == self.region.size,
                        "gzip has trailing bytes or multiple members")
                self.finished = True
        return bytes(result)

    def exact(self, size):
        result = self.read(size)
        require(len(result) == size, "truncated tar archive")
        return result


def tar_number(field):
    if field[0] & 128:
        require(not field[0] & 64, "negative tar size")
        return int.from_bytes(bytes([field[0] & 127]) + field[1:], "big")
    value = field.rstrip(b"\0 ").lstrip(b" ")
    require(not value or all(c in b"01234567" for c in value), "invalid tar number")
    return int(value or b"0", 8)


def looks_like_tar(source, offset, size):
    try:
        header = zlib.decompressobj(31).decompress(source.read(offset, min(65536, size)), 512)
        return (len(header) == 512 and any(header) and tar_number(header[148:156]) ==
                sum(header[:148]) + 256 + sum(header[156:]))
    except (Invalid, zlib.error):
        return False


def safe_name(name, allow_root=False):
    require(len(name) <= 4096 and "\0" not in name and "\\" not in name,
            "invalid archive member name")
    while name.startswith("./"):
        name = name[2:]
    name = name.rstrip("/")
    require(not name.startswith("/") and ".." not in name.split("/"), "unsafe archive member path")
    require(bool(name) or allow_root, "empty archive member name")
    return name


def pax_fields(data):
    result, pos = {}, 0
    while pos < len(data):
        space = data.find(b" ", pos)
        require(pos < space < pos + 16 and data[pos:space].isdigit(), "invalid PAX length")
        end = pos + int(data[pos:space])
        require(space + 1 < end <= len(data) and data[end - 1] == 10, "invalid PAX record")
        key, separator, value = data[space + 1:end - 1].partition(b"=")
        require(separator, "invalid PAX key")
        result[key.decode("utf-8")] = value.decode("utf-8")
        require(len(result) <= 64, "too many PAX fields")
        pos = end
    return result


def tar_body(stream, size, capture=False):
    require(0 <= size <= MAX_INFLATED, "oversized tar member")
    require(not capture or size <= MAX_METADATA, "oversized tar metadata")
    captured = bytearray()
    remaining = size
    while remaining:
        chunk = stream.exact(min(CHUNK, remaining))
        if capture:
            captured.extend(chunk)
        remaining -= len(chunk)
    if size % 512:
        require(not any(stream.exact(512 - size % 512)), "nonzero tar member padding")
    return bytes(captured)


def validate_tar(source, candidate, budget):
    stream = GzipStream(Region(source, candidate["offset"], candidate["size"]), budget)
    names, regular, tree = set(), 0, None
    global_pax, local_pax, long_name = {}, {}, None
    count = 0
    while True:
        header = stream.exact(512)
        if not any(header):
            require(not any(stream.exact(512)), "tar needs two terminating zero blocks")
            while not stream.finished:
                require(not any(stream.read(CHUNK)), "nonzero data after tar terminator")
            break
        count += 1
        require(count <= MAX_MEMBERS, "too many tar members")
        require(tar_number(header[148:156]) == sum(header[:148]) + 256 + sum(header[156:]),
                "tar header checksum mismatch")
        size, kind = tar_number(header[124:136]), header[156:157]
        name = header[:100].split(b"\0", 1)[0]
        if header[257:263] == b"ustar\0":
            prefix = header[345:500].split(b"\0", 1)[0]
            name = prefix + b"/" + name if prefix else name
        if kind in (b"x", b"g", b"L", b"K"):
            body = tar_body(stream, size, capture=True)
            if kind == b"g":
                global_pax = {**global_pax, **pax_fields(body)}
                require(len(global_pax) <= 64, "too many global PAX fields")
            elif kind == b"x":
                local_pax = pax_fields(body)
            elif kind == b"L":
                require(long_name is None, "duplicate pending GNU long name")
                long_name = body.rstrip(b"\0").decode("utf-8")
            continue
        require(kind in (b"\0", b"0", b"1", b"2", b"3", b"4", b"5", b"6", b"7"),
                "unsupported tar member type")
        overrides = {**global_pax, **local_pax}
        name = safe_name(overrides.get("path", long_name or name.decode("utf-8")), kind == b"5")
        if "size" in overrides:
            require(overrides["size"].isdigit(), "invalid PAX size")
            size = int(overrides["size"])
        require(not any(k.startswith("GNU.sparse") for k in overrides), "sparse tar is unsupported")
        require(name not in names, "duplicate tar member: " + name)
        names.add(name)
        is_regular = kind in (b"\0", b"0", b"7")
        require(is_regular or size == 0, "non-file tar member has data")
        body = tar_body(stream, size, capture=name == "TREE.md")
        if is_regular:
            regular += 1
        if name == "TREE.md":
            require(is_regular, "TREE.md must be a regular file")
            tree = body.decode("utf-8")
        local_pax, long_name = {}, None
    require(not local_pax and long_name is None, "dangling tar metadata")
    prefixes = {"n9e": "# nightingale (n9e) code corpus — ",
                "categraf": "# categraf code corpus — ", "fe": "# n9e-fe code corpus — "}
    matches = [repo for repo, prefix in prefixes.items() if tree and tree.startswith(prefix)]
    if matches:
        repo = matches[0]
        require(regular == len(names), "code corpus contains non-regular members")
        require(not any(n.startswith(("vendor/", "src/plus/")) for n in names),
                "unexpected excluded source tree in corpus")
        if repo == "fe":
            require(any(n.startswith("src/") and n.endswith((".ts", ".tsx")) for n in names),
                    "frontend corpus has no TS source")
        else:
            require(any(n.endswith(".go") for n in names), "Go corpus has no Go source")
        return repo, {"files": regular, "tree_ref": tree.splitlines()[0][len(prefixes[repo]):]}
    require("etc/os-release" in names and any(n.startswith("usr/local/bin/python3") for n in names)
            and any(n.startswith("usr/local/lib/python3") for n in names),
            "gzip TAR is neither a code corpus nor the Python rootfs")
    return "python", {"members": len(names), "files": regular}


def unique_json(data):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def validate_manifest(source, candidate):
    manifest = unique_json(source.read(candidate["offset"], candidate["size"]))
    require(isinstance(manifest, list) and len(manifest) == 3, "unexpected QA manifest")
    result = {}
    for entry in manifest:
        require(isinstance(entry, dict) and entry.get("repo") in ("n9e", "categraf", "fe"),
                "unknown QA repository")
        repo = entry["repo"]
        require(repo not in result, "duplicate QA repository")
        require(isinstance(entry.get("commit"), str) and
                re.fullmatch("[0-9a-f]{40}", entry["commit"]) is not None,
                "QA source commit must be a full SHA")
        require(isinstance(entry.get("ref"), str) and entry["ref"], "missing QA source ref")
        require(type(entry.get("files")) is int and 0 < entry["files"] <= MAX_MEMBERS,
                "invalid QA file count")
        result[repo] = entry
    require(result["n9e"]["commit"] == SOURCE_COMMIT, "QA n9e source commit mismatch")
    return result


def classify_elf(source, candidate):
    embedded = Elf(source, candidate["offset"], candidate["size"])
    if ".go.buildinfo" in embedded.named:
        info = embedded.buildinfo()
        require(info.get("path") == MODULE + "/cmd/n9e-sandbox-init", "unexpected embedded Go ELF")
        require(info["settings"].get("GOARCH") == "amd64" and
                info["settings"].get("CGO_ENABLED") == "0", "unexpected sandbox init build")
        return "init", {"go_version": info["go_version"], "path": info["path"]}
    found = False
    for pos in range(0, embedded.size, CHUNK):
        if b"bubblewrap" in embedded.read(pos, min(CHUNK + 16, embedded.size - pos)):
            found = True
    require(found, "embedded non-Go ELF is not identifiable as bubblewrap")
    return "bwrap", {"elf_machine": "amd64", "elf_type": "ET_EXEC"}


def find_magic(source, magic):
    overlap = len(magic) - 1
    for base in range(0, source.size, CHUNK):
        data = source.read(base, min(CHUNK + overlap, source.size - base))
        start = 0
        while True:
            pos = data.find(magic, start)
            if pos < 0 or pos >= CHUNK:
                break
            yield base + pos
            start = pos + 1


def validate_zip(source, offset, size, expected_count, budget):
    with zipfile.ZipFile(Region(source, offset, size)) as archive:
        entries = archive.infolist()
        require(len(entries) == expected_count and 0 < len(entries) <= MAX_MEMBERS,
                "unexpected ZIP entry count")
        names, index_hash = set(), None
        for entry in entries:
            name = safe_name(entry.filename)
            require(name == entry.filename and name not in names, "noncanonical or duplicate ZIP name")
            names.add(name)
            require(not entry.is_dir() and entry.compress_type in (0, 8) and not entry.flag_bits & 1,
                    "unsupported statik ZIP entry")
            require(not stat.S_ISLNK(entry.external_attr >> 16), "ZIP symlink is unsupported")
            require(entry.file_size <= MAX_ASSET, "oversized frontend file")
            digest, total = hashlib.sha256(), 0
            with archive.open(entry) as member:
                while True:
                    data = member.read(CHUNK)
                    if not data:
                        break
                    budget.add(len(data))
                    total += len(data)
                    require(total <= entry.file_size, "ZIP inflated size mismatch")
                    digest.update(data)
            require(total == entry.file_size, "ZIP file size mismatch")
            if name == "index.html":
                index_hash = digest.hexdigest()
        require(index_hash and any(n.endswith(".js") for n in names), "ZIP is not a frontend bundle")
        return {"files": len(entries), "index_html_sha256": index_hash}


def frontend_candidate(source, budget):
    matches, attempts = [], 0
    for end in find_magic(source, b"PK\x05\x06"):
        if end + 22 > source.size:
            continue
        fields = struct.unpack("<4s4H2IH", source.read(end, 22))
        _, disk, central_disk, count_disk, count, central_size, central_offset, comment_size = fields
        start = end - central_size - central_offset
        size = end + 22 + comment_size - start
        if (disk or central_disk or count_disk != count or not 0 < count < 65535 or
                not 0 < central_size <= 16 << 20 or not 0 <= start < end or
                not 0 < size <= MAX_ASSET or start + size > source.size):
            continue
        if source.read(start, 4) != b"PK\x03\x04" or source.read(end - central_size, 4) != b"PK\x01\x02":
            continue
        attempts += 1
        require(attempts <= 16, "too many structural ZIP candidates")
        # Check index presence before streaming other embedded ZIPs.
        with zipfile.ZipFile(Region(source, start, size)) as archive:
            if "index.html" not in archive.namelist():
                continue
        detail = validate_zip(source, start, size, count, budget)
        matches.append(({"offset": start, "size": size}, detail))
    require(len(matches) == 1, "expected exactly one valid frontend ZIP with index.html")
    return matches[0]


def inspect_assets(source):
    elf = Elf(source)
    build = validate_buildinfo(elf.buildinfo())
    budget, assets, detail = Budget(), {}, {}
    for candidate in slice_candidates(elf):
        if candidate["kind"] == "gzip":
            name, metadata = validate_tar(source, candidate, budget)
        elif candidate["kind"] == "manifest":
            name, metadata = "manifest", validate_manifest(source, candidate)
        else:
            name, metadata = classify_elf(source, candidate)
        require(name not in assets, "ambiguous embedded asset: " + name)
        assets[name], detail[name] = candidate, metadata
    assets["frontend"], detail["frontend"] = frontend_candidate(source, budget)
    require(set(assets) == set(PATHS), "missing assets: " + ", ".join(sorted(set(PATHS) - set(assets))))
    for repo in ("n9e", "categraf", "fe"):
        require(detail[repo]["files"] == detail["manifest"][repo]["files"],
                "QA manifest file count mismatch: " + repo)
        require(detail[repo]["tree_ref"] == detail["manifest"][repo]["ref"],
                "QA manifest ref mismatch: " + repo)
    for asset in assets.values():
        asset["sha256"] = source.digest(asset["offset"], asset["size"])
    return assets, detail, build, budget.used


def write_assets(source, output, assets):
    os.mkdir(output, 0o700)
    for name, relative in PATHS.items():
        asset = assets[name]
        destination = output / relative
        destination.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
        digest = hashlib.sha256()
        with destination.open("xb") as target:
            for pos in range(asset["offset"], asset["offset"] + asset["size"], CHUNK):
                data = source.read(pos, min(CHUNK, asset["offset"] + asset["size"] - pos))
                target.write(data)
                digest.update(data)
        require(digest.hexdigest() == asset["sha256"], "binary asset changed while copying")


def run():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="/app/n9e", help="read-only official center ELF")
    parser.add_argument("--output", required=True, type=Path, help="new output directory (must not exist)")
    parser.add_argument("--expected-binary-sha256", required=True,
                        help="SHA256 of /app/n9e copied from the pinned official image")
    args = parser.parse_args()
    require(re.fullmatch("[0-9a-f]{64}", args.expected_binary_sha256) is not None,
            "expected binary SHA256 must be 64 lowercase hexadecimal characters")
    require(not os.path.lexists(args.output), "output directory already exists")
    source = Source(args.binary)
    try:
        binary_sha = source.digest(0, source.size)
        require(binary_sha == args.expected_binary_sha256, "official binary SHA256 mismatch")
        assets, detail, build, inflated = inspect_assets(source)
        source.unchanged()
        write_assets(source, args.output, assets)
        require(source.digest(0, source.size) == binary_sha, "binary changed during extraction")
        source.unchanged()
        manifest = {
            "schema_version": 1, "binary_sha256": binary_sha, "binary_size": source.size,
            "build_info": build, "source_commit": SOURCE_COMMIT,
            "assets": {PATHS[name]: {k: asset[k] for k in
                       ("sha256", "size", "offset", "slice_header_offsets") if k in asset}
                       for name, asset in assets.items()},
            "qa_manifest": detail["manifest"],
            "frontend_index_html_sha256": detail["frontend"]["index_html_sha256"],
            "frontend_file_count": detail["frontend"]["files"],
            "validated_inflated_bytes": inflated,
        }
        temporary_manifest = args.output / ".extraction.json.tmp"
        with temporary_manifest.open("x", encoding="utf-8") as target:
            json.dump(manifest, target, indent=2, sort_keys=True)
            target.write("\n")
            target.flush()
            os.fsync(target.fileno())
        # Linking is atomic and refuses an existing completion record. A failed
        # JSON write can never leave a misleading extraction.json behind.
        os.link(temporary_manifest, args.output / "extraction.json")
        temporary_manifest.unlink()
        print(json.dumps({"output": str(args.output), "binary_sha256": binary_sha,
                          "assets": len(assets), "source_commit": SOURCE_COMMIT}))
    finally:
        os.close(source.fd)


if __name__ == "__main__":
    try:
        run()
    except (Invalid, OSError, UnicodeError, json.JSONDecodeError, zipfile.BadZipFile,
            zlib.error, struct.error) as error:
        print("asset extraction failed: " + str(error), file=sys.stderr)
        sys.exit(1)
