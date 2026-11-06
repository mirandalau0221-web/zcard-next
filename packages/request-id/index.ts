/** Random request identifiers also work on HTTP installations without randomUUID. */
export function newRequestId(): string {
  const provider = globalThis.crypto;
  if (typeof provider?.randomUUID === "function") return provider.randomUUID();
  if (typeof provider?.getRandomValues !== "function") {
    throw new Error("瀏覽器不支持安全生成請求標識，請更換瀏覽器後重試");
  }
  const bytes = provider.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}
