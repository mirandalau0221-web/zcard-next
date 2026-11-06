export type ShippingAddress = Record<
  "country" | "region" | "city" | "district" | "address" | "name" | "phone" | "postal_code",
  string
> & { address_line2?: string };
export type RegionData = Record<string, Record<string, string>>;
export const emptyAddress = (): ShippingAddress => ({
  country: "",
  region: "",
  city: "",
  district: "",
  address: "",
  address_line2: "",
  name: "",
  phone: "",
  postal_code: "",
});
export function countryOptions(data: RegionData, allowed?: string[], locale = "zh-TW") {
  const names = new Intl.DisplayNames([locale], { type: "region" });
  return Object.keys(data)
    .filter((code) => !allowed || allowed.includes(code))
    .map((value) => ({
      value,
      label: `${names.of(value) || data[value].name || value} (${value})`,
    }))
    .sort((a, b) => a.label.localeCompare(b.label, locale));
}
export function regionOptions(data: RegionData, country: string, locale = "zh-TW") {
  const info = data[country] || {};
  const keys = (info.sub_keys || "").split("~").filter(Boolean);
  const english = locale.startsWith("en");
  const names = (english ? info.sub_lnames || info.sub_names || "" : info.sub_names || info.sub_lnames || "").split("~");
  return keys.map((value, i) => {
    const name = names[i] || value;
    // Some countries provide English subdivision keys but only local display names.
    const label = english && !info.sub_lnames && /[^\u0000-\u024f]/.test(name) && /^[\u0000-\u024f]+$/.test(value) ? value : name;
    return { value, label };
  });
}
export type AddressField = "city" | "district" | "region" | "postal_code";
export function addressFields(data: RegionData, country: string): AddressField[] {
  const info = data[country] || {};
  const required = info.require || "AC";
  const format = info.fmt || "%N%n%O%n%A%n%C";
  return ([['city', 'C'], ['district', 'D'], ['region', 'S'], ['postal_code', 'Z']] as const)
    .filter(([, code]) => required.includes(code) || format.includes(`%${code}`))
    .map(([field]) => field);
}
export function addressRequired(data: RegionData, country: string, field: AddressField): boolean {
  const codes: Record<AddressField, string> = { city: "C", district: "D", region: "S", postal_code: "Z" };
  return (data[country]?.require || "AC").includes(codes[field]);
}
/** Keep district independent of the optional apartment/unit address line. */
export function addressLines(address: Record<string, string | undefined>): string[] {
  return [address.address, address.address_line2,
    [address.district, address.city, address.region, address.postal_code].filter(Boolean).join(" "),
    address.country].filter((line): line is string => Boolean(line));
}
export function validateAddress(address: Record<string, string | undefined>, data: RegionData): Record<string, string> {
  const errors: Record<string, string> = {};
  const requiredMessages: Record<string, string> = {
    country: "請選擇國家或地區。", name: "請填寫收貨人姓名。", phone: "請填寫電話號碼。", address: "請填寫地址第一行。",
    city: "請填寫郊區或城市。", district: "請填寫區或縣。", region: "請選擇州、省或地區。", postal_code: "請填寫郵政編碼。",
  };
  const info = data[address.country || ""];
  if (!info) errors.country = requiredMessages.country;
  const required = ["name", "phone", "address", ...addressFields(data, address.country || "").filter((field) => addressRequired(data, address.country || "", field))];
  for (const key of required) if (!address[key]?.trim()) errors[key] = requiredMessages[key];
  for (const [key, value] of Object.entries(address)) {
    if (value && (Array.from(value.trim()).length > 300 || /[\u0000\r\n]/.test(value))) errors[key] = "此字段不能超過 300 個字符或包含換行和控制字符。";
  }
  const phone = address.phone?.trim() || "";
  const digits = phone.replace(/\D/g, "").length;
  if (phone && (digits < 6 || digits > 15 || !/^\+?[0-9 ()\-]{6,30}$/.test(phone))) errors.phone = "電話號碼格式無效，請包含國際區號。";
  const states = regionOptions(data, address.country || "");
  if (address.region && states.length && !states.some((state) => state.value === address.region)) errors.region = "請選擇屬於該國家或地區的州、省或地區。";
  if (info?.zip && address.postal_code?.trim()) {
    try {
      if (!new RegExp(`^(?:${info.zip})$`, "i").test(address.postal_code.trim())) errors.postal_code = "郵政編碼與所選國家或地區的格式不符。";
    } catch { /* The server remains authoritative for metadata not supported by JS. */ }
  }
  return errors;
}
export function shippingStatus(value?: string) {
  return (
    (
      {
        none: "無需配送",
        pending_payment: "待付款",
        pending: "待發貨",
        partial: "部分發貨",
        shipped: "已發貨，待收貨",
        received: "已收貨",
        canceled: "配送已取消",
      } as Record<string, string>
    )[value || "none"] || value
  );
}
export interface Shipment {
  id: number;
  carrier: string;
  tracking_no: string;
  items: Record<string, number>;
  status: string;
  created_at: number;
  received_at: number;
}
export function shipments(raw?: string): Shipment[] {
  try {
    const rows = JSON.parse(raw || "[]");
    return Array.isArray(rows) ? rows : [];
  } catch {
    return [];
  }
}
