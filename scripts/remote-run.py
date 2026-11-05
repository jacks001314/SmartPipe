#!/usr/bin/env python3
"""Upload a tar.gz to a Linux host, extract it, and run a command.

This helper is used by deployment and Linux SIT scripts. The optional --sudo
flag forwards the same SSH password to sudo -S without printing it.
"""
import argparse
import os
import shlex
import sys

import paramiko


def main() -> int:
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if reconfigure is not None:
            reconfigure(errors="replace")
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", required=True)
    ap.add_argument("--user", required=True)
    ap.add_argument("--port", type=int, default=22)
    ap.add_argument("--pass-file", default="")
    ap.add_argument("--local", required=True, help="local tar.gz")
    ap.add_argument("--dir", default="/tmp/smartpipe-sit", help="remote extract directory below /tmp")
    ap.add_argument("--cmd", required=True, help="command run after extraction")
    ap.add_argument("--sudo", action="store_true", help="run --cmd through sudo")
    ap.add_argument("--cleanup", action="store_true", help="remove the uploaded archive and extract directory")
    args = ap.parse_args()

    password = os.environ.get("SMARTPIPE_SSH_PASSWORD", "")
    if args.pass_file:
        with open(args.pass_file, encoding="utf-8") as fh:
            password = fh.read().strip()
    if args.sudo and not password:
        print("[sudo] --sudo requires --pass-file or SMARTPIPE_SSH_PASSWORD", file=sys.stderr)
        return 2

    remote_dir = args.dir.rstrip("/")
    if not remote_dir.startswith("/tmp/") or ".." in remote_dir.split("/"):
        print("[extract] --dir must be below /tmp", file=sys.stderr)
        return 2

    remote_tar = "/tmp/" + os.path.basename(args.local)
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(args.host, port=args.port, username=args.user, password=password,
                   timeout=15, allow_agent=False, look_for_keys=False)
    try:
        with client.open_sftp() as sftp:
            sftp.put(args.local, remote_tar)
            size = sftp.stat(remote_tar).st_size
        print(f"[upload] {args.local} -> {remote_tar} ({size} bytes)")

        def run(cmd: str) -> int:
            _, out, err = client.exec_command(cmd, get_pty=False)
            for line in iter(out.readline, ""):
                sys.stdout.write(line)
            sys.stdout.flush()
            e = err.read().decode("utf-8", "replace")
            if e.strip():
                sys.stderr.write(e)
            return out.channel.recv_exit_status()

        extract = (
            f"rm -rf -- {shlex.quote(remote_dir)} && "
            f"mkdir -p {shlex.quote(remote_dir)} && "
            f"tar -xzf {shlex.quote(remote_tar)} -C {shlex.quote(remote_dir)}"
        )
        rc = run(extract)
        if rc != 0:
            print(f"[extract] failed rc={rc}")
            return rc
        command = f"cd {shlex.quote(remote_dir)} && {args.cmd}"
        if args.sudo:
            command = f"printf '%s\\n' {shlex.quote(password)} | sudo -S -p '' bash -lc {shlex.quote(command)}"
        rc = run(command)
        print(f"[remote] exit={rc}")
        if args.cleanup:
            run(f"rm -rf -- {shlex.quote(remote_dir)} {shlex.quote(remote_tar)}")
        return rc
    finally:
        client.close()


if __name__ == "__main__":
    sys.exit(main())
