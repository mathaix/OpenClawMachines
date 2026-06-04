export interface WsFrame {
  type: string;
  payload: string | ArrayBuffer;
  raw: MessageEvent;
}

export function parseFrame(event: MessageEvent): WsFrame {
  if (typeof event.data === "string") {
    if (event.data.length > 0) {
      return { type: event.data[0], payload: event.data.slice(1), raw: event };
    }
    return { type: "", payload: "", raw: event };
  }
  return { type: "binary", payload: event.data, raw: event };
}
