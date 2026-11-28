import type { ChatAttachment } from "./types";

// Files live only as long as the pending attachment. No preview URL or image
// bytes are serialized with a chat message.
export const pendingImageFiles = new WeakMap<ChatAttachment, File>();
export function isChatImage(a: ChatAttachment) {
  return !!a.image_mime || /\.(png|jpe?g|webp)$/i.test(a.path);
}
