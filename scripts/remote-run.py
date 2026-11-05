#!/usr/bin/env python3
"""把本地 tar 包传到 Linux 主机、解包并执行命令（开发/联调辅助）。

用法示例：
  python scripts/remote-run.py --host 192.168.1.99 --user jacks \
      --pass-file /path/pass.txt --local .dev/sit-linux.tar.gz \
      --dir /tmp/sit --cmd "bash scripts/sit-linux.sh --skip-test"
"""
import argparse
import os
import sys

import paramiko


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", required=True)
    ap.add_argument("--user", required=True)
    ap.add_argument("--port", type=int, default=22)
    ap.add_argument("--pass-file", default="")
    ap.add_argument("--local", required=True, help="本地 tar.gz")
    ap.add_argument("--dir", default="/tmp/smartpipe-sit", help="远端解包目录")
    ap.add_argument("--cmd", required=True, help="解包后执行的命令")
    args = ap.parse_args()

    password = os.environ.get("SMARTPIPE_SSH_PASSWORD", "")
    if args.pass_file:
        with open(args.pass_file, encoding="utf-8") as fh:
            password = fh.read().strip()

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
            rc = out.channel.recv_exit_status()
            return rc

        rc = run(f"rm -rf {args.dir} && mkdir -p {args.dir} && tar -xzf {remote_tar} -C {args.dir}")
        if rc != 0:
            print(f"[extract] failed rc={rc}")
            return rc
        rc = run(f"cd {args.dir} && {args.cmd}")
        print(f"[remote] exit={rc}")
        return rc
    finally:
        client.close()


if __name__ == "__main__":
    sys.exit(main())