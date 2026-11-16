import type { zh } from "./dictionaries/zh";

type DeepStrings<T> = {
  [K in keyof T]: T[K] extends string ? string : DeepStrings<T[K]>;
};

/** Shape of every dictionary (derived from the zh source of truth). */
export type Dictionary = DeepStrings<typeof zh>;

type Leaves<T, P extends string = ""> = {
  [K in keyof T & string]: T[K] extends string ? `${P}${K}` : Leaves<T[K], `${P}${K}.`>;
}[keyof T & string];

/** Every translatable key, e.g. "checkout.pay.sendExactly". */
export type MessageKey = Leaves<Dictionary>;

export type MessageVars = Record<string, string | number>;

export type TranslateFn = (key: MessageKey, vars?: MessageVars) => string;
