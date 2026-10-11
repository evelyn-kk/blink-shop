#!/usr/bin/env python3
"""真机验收用的局域网转发：在本机局域网地址上监听，转发到只绑 127.0.0.1 的验收部署。

真机要测真实断网（关 Wi-Fi 再打开）时，App 必须走 Wi-Fi 访问服务端，不能走 USB 的 adb reverse。
Docker 跑在 colima 等虚拟机里时，容器端口没法直接绑到宿主机的局域网地址，所以在宿主机上转发一层。
同一网络的设备都能访问监听地址，只在验收期间运行，用完 Ctrl+C（或 kill）停掉。

  python3 quality/acceptance/lan_forward.py 192.168.8.107:18188 127.0.0.1:18088
"""
import asyncio
import sys


async def pipe(reader, writer):
    try:
        while data := await reader.read(65536):
            writer.write(data)
            await writer.drain()
    except (ConnectionError, asyncio.CancelledError):
        pass
    finally:
        writer.close()


async def main(listen, target):
    lhost, lport = listen.rsplit(":", 1)
    thost, tport = target.rsplit(":", 1)

    async def handle(cr, cw):
        try:
            tr, tw = await asyncio.open_connection(thost, int(tport))
        except OSError:
            cw.close()
            return
        await asyncio.gather(pipe(cr, tw), pipe(tr, cw))

    server = await asyncio.start_server(handle, lhost, int(lport))
    print(f"转发 {listen} -> {target}（Ctrl+C 停止）", flush=True)
    async with server:
        await server.serve_forever()


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    try:
        asyncio.run(main(sys.argv[1], sys.argv[2]))
    except KeyboardInterrupt:
        pass
