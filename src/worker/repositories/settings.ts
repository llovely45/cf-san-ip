export interface AppSettings {
  adminPassword: string | null;
  apiToken: string | null;
}

interface AppSettingsRow {
  admin_password: string | null;
  api_token: string | null;
}

const SETTINGS_ROW_ID = 1;

const toAppSettings = (row: AppSettingsRow): AppSettings => ({
  adminPassword: row.admin_password,
  apiToken: row.api_token,
});

export class D1SettingsRepository {
  constructor(private readonly db: D1Database) {}

  async get(): Promise<AppSettings> {
    const row = await this.db
      .prepare(
        `SELECT admin_password, api_token
         FROM app_settings
         WHERE singleton_id = ?`,
      )
      .bind(SETTINGS_ROW_ID)
      .first<AppSettingsRow>();

    if (row === null) {
      throw new Error("app settings row is missing");
    }

    return toAppSettings(row);
  }

  async initializeAdmin(password: string): Promise<boolean> {
    const result = await this.db
      .prepare(
        `UPDATE app_settings
         SET admin_password = ?
         WHERE singleton_id = ? AND admin_password IS NULL`,
      )
      .bind(password, SETTINGS_ROW_ID)
      .run();

    return result.meta.changes === 1;
  }

  async setAdminPassword(password: string | null): Promise<void> {
    await this.db
      .prepare(
        `UPDATE app_settings
         SET admin_password = ?
         WHERE singleton_id = ?`,
      )
      .bind(password, SETTINGS_ROW_ID)
      .run();
  }

  async setApiToken(apiToken: string | null): Promise<void> {
    await this.db
      .prepare(
        `UPDATE app_settings
         SET api_token = ?
         WHERE singleton_id = ?`,
      )
      .bind(apiToken, SETTINGS_ROW_ID)
      .run();
  }
}

export async function initializeAdmin(
  db: D1Database,
  password: string,
): Promise<boolean> {
  return new D1SettingsRepository(db).initializeAdmin(password);
}
