import { getToken } from "./api";
import type { SchedulerOutputFrame } from "./types";

const MAX_FRAME_BYTES = 4 * 1024 * 1024;

export class SchedulerOutputStreamError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

type Frame = {
  event: string;
  id: string;
  data: string;
};

/** 按 SSE 的双换行边界切帧，并限制坏流积累的内存。 */
export class SchedulerSseBuffer {
  private buffer = "";

  append(chunk: string): Frame[] {
    this.buffer += chunk.replace(/\r\n/g, "\n");
    const frames: Frame[] = [];
    let boundary = this.buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const raw = this.buffer.slice(0, boundary);
      this.buffer = this.buffer.slice(boundary + 2);
      if (raw && !raw.startsWith(":")) {
        const frame: Frame = { event: "message", id: "", data: "" };
        const data: string[] = [];
        for (const line of raw.split("\n")) {
          if (line.startsWith("event:")) frame.event = line.slice(6).trim();
          else if (line.startsWith("id:")) frame.id = line.slice(3).trim();
          else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
        }
        frame.data = data.join("\n");
        frames.push(frame);
      }
      boundary = this.buffer.indexOf("\n\n");
    }
    if (this.buffer.length > MAX_FRAME_BYTES) {
      this.buffer = "";
      throw new SchedulerOutputStreamError(0, "实时输出单帧过大，正在重新连接");
    }
    return frames;
  }
}

export async function consumeSchedulerOutputStream(options: {
  workerId: string;
  after: number;
  signal: AbortSignal;
  onFrame: (frame: SchedulerOutputFrame) => void;
  onDone: () => void;
}): Promise<void> {
  const headers: Record<string, string> = { Accept: "text/event-stream" };
  const token = getToken();
  if (token) headers.Authorization = "Bearer " + token;
  const response = await fetch(
    "/admin/scheduler/workers/" + encodeURIComponent(options.workerId) + "/stream?after=" + options.after,
    { headers, credentials: "same-origin", signal: options.signal },
  );
  if (!response.ok) {
    let message = "实时输出连接失败（HTTP " + response.status + "）";
    try {
      const payload = await response.json() as { error?: { message?: string } };
      if (payload.error?.message) message = payload.error.message;
    } catch {
      // 非 JSON 错误体沿用状态码提示。
    }
    throw new SchedulerOutputStreamError(response.status, message);
  }
  if (!response.body) throw new SchedulerOutputStreamError(0, "浏览器没有提供可读的实时输出流");

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  const buffer = new SchedulerSseBuffer();
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) return;
      const frames = buffer.append(decoder.decode(value, { stream: true }));
      for (const frame of frames) {
        if (frame.event === "done") {
          options.onDone();
          return;
        }
        if (frame.event !== "outputs" || !frame.data) continue;
        const payload = JSON.parse(frame.data) as SchedulerOutputFrame;
        options.onFrame(payload);
      }
    }
  } finally {
    reader.releaseLock();
  }
}
