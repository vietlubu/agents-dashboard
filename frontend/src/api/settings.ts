// Settings API: general settings, scan roots, prices and price rules, and the data reset.
import * as Settings from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/settingsservice";
import type * as StoreModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/store/models";
import type * as PricingModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/pricing/models";

export type SettingsModel = StoreModels.Settings;
export type SettingsPatch = StoreModels.SettingsPatch;
export type ScanRoot = StoreModels.ScanRoot;
export type ModelPrice = StoreModels.ModelPrice;
export type PriceRule = StoreModels.PriceRule;
export type Stats = StoreModels.Stats;
export type SyncResult = PricingModels.SyncResult;

export const get = Settings.Get as () => Promise<SettingsModel>;
export const update = Settings.Update as (patch: SettingsPatch) => Promise<SettingsModel>;
export const savePrice = Settings.SetPrice as (price: ModelPrice) => Promise<ModelPrice[]>;
export const savePriceRule = Settings.SetPriceRule as (rule: PriceRule) => Promise<PriceRule[]>;
export const syncPrices = Settings.SyncPrices as (source: string) => Promise<SyncResult>;
export const stats = Settings.Stats as () => Promise<Stats>;

// List responses: null (no rows) is normalised to an empty array.

export async function scanRoots(): Promise<ScanRoot[]> {
  return (await Settings.ScanRoots()) ?? [];
}

export async function addScanRoot(harness: string, path: string): Promise<ScanRoot[]> {
  return (await Settings.AddScanRoot(harness, path)) ?? [];
}

export async function removeScanRoot(harness: string, path: string): Promise<ScanRoot[]> {
  return (await Settings.RemoveScanRoot(harness, path)) ?? [];
}

export async function prices(): Promise<ModelPrice[]> {
  return (await Settings.Prices()) ?? [];
}

export async function deletePrice(modelKey: string): Promise<ModelPrice[]> {
  return (await Settings.DeletePrice(modelKey)) ?? [];
}

export async function priceRules(): Promise<PriceRule[]> {
  return (await Settings.PriceRules()) ?? [];
}

export const recalculateCosts = Settings.RecalculateCosts as () => Promise<number>;
export const deleteAllData = Settings.DeleteAllData as () => Promise<void>;