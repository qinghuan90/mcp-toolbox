PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, username TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('admin','user')), enabled INTEGER NOT NULL DEFAULT 1, failures INTEGER NOT NULL DEFAULT 0, locked_until DATETIME, must_change_password INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),expires_at DATETIME NOT NULL);
CREATE TABLE IF NOT EXISTS tokens(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),client_name TEXT NOT NULL,hash TEXT UNIQUE NOT NULL,expires_at DATETIME NOT NULL,revoked INTEGER NOT NULL DEFAULT 0,created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,last_seen_at DATETIME,client_deleted INTEGER NOT NULL DEFAULT 0,never_expires INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS instances(id TEXT PRIMARY KEY,name TEXT NOT NULL,transport TEXT NOT NULL,config BLOB NOT NULL,secret BLOB NOT NULL,version INTEGER NOT NULL DEFAULT 1,enabled INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT 'stopped',last_error TEXT NOT NULL DEFAULT '',created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,runtime_generation TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS config_versions(instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,version INTEGER NOT NULL,config BLOB NOT NULL,secret BLOB NOT NULL,PRIMARY KEY(instance_id,version));
CREATE TABLE IF NOT EXISTS rules(id TEXT PRIMARY KEY,instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,tool_name TEXT NOT NULL DEFAULT '',effect TEXT NOT NULL CHECK(effect IN ('allow','deny')),UNIQUE(instance_id,tool_name,effect));
CREATE TABLE IF NOT EXISTS ip_rules(id TEXT PRIMARY KEY,cidr TEXT NOT NULL,effect TEXT NOT NULL CHECK(effect IN ('allow','deny')),created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(cidr,effect));
CREATE TABLE IF NOT EXISTS logs(id TEXT PRIMARY KEY,user_id TEXT,token_id TEXT,client_name TEXT NOT NULL DEFAULT '',instance_id TEXT,tool_name TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '',duration_ms INTEGER NOT NULL DEFAULT 0,created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,runtime_generation TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS logs_user_time ON logs(user_id,created_at DESC);
CREATE INDEX IF NOT EXISTS logs_uncertain_generation ON logs(instance_id,runtime_generation) WHERE status IN ('accepted','unknown');
CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,kind TEXT NOT NULL,status TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '',result BLOB NOT NULL DEFAULT '{}',created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS discoveries(id TEXT PRIMARY KEY,source TEXT NOT NULL,name TEXT NOT NULL,config BLOB NOT NULL,secret BLOB,fingerprint TEXT NOT NULL DEFAULT '',warnings BLOB NOT NULL DEFAULT '[]',importable INTEGER NOT NULL DEFAULT 0,imported_instance_id TEXT REFERENCES instances(id) ON DELETE SET NULL,scanned_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(source,name));
CREATE TABLE IF NOT EXISTS scan_roots(path TEXT PRIMARY KEY,created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS system_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS management_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,entity_type TEXT NOT NULL,entity_id TEXT NOT NULL,action TEXT NOT NULL,created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TRIGGER IF NOT EXISTS audit_scan_roots_insert AFTER INSERT ON scan_roots BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('scan_roots',new.path,'INSERT'); END;
CREATE TRIGGER IF NOT EXISTS audit_scan_roots_delete AFTER DELETE ON scan_roots BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('scan_roots',old.path,'DELETE'); END;
CREATE TRIGGER IF NOT EXISTS audit_users_insert AFTER INSERT ON users BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('users',new.id,'INSERT'); END;
CREATE TRIGGER IF NOT EXISTS audit_users_update AFTER UPDATE ON users WHEN new.role<>old.role OR new.enabled<>old.enabled OR new.password_hash<>old.password_hash BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('users',new.id,'UPDATE'); END;
CREATE TRIGGER IF NOT EXISTS audit_users_delete AFTER DELETE ON users BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('users',old.id,'DELETE'); END;
CREATE TRIGGER IF NOT EXISTS audit_tokens_insert AFTER INSERT ON tokens BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('tokens',new.id,'INSERT'); END;
CREATE TRIGGER IF NOT EXISTS audit_tokens_update AFTER UPDATE ON tokens WHEN new.revoked<>old.revoked BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('tokens',new.id,'UPDATE'); END;
CREATE TRIGGER IF NOT EXISTS audit_tokens_delete AFTER DELETE ON tokens BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('tokens',old.id,'DELETE'); END;
CREATE TRIGGER IF NOT EXISTS audit_rules_insert AFTER INSERT ON rules BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('rules',new.id,'INSERT'); END;
CREATE TRIGGER IF NOT EXISTS audit_rules_update AFTER UPDATE ON rules BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('rules',new.id,'UPDATE'); END;
CREATE TRIGGER IF NOT EXISTS audit_rules_delete AFTER DELETE ON rules BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('rules',old.id,'DELETE'); END;
CREATE TRIGGER IF NOT EXISTS audit_ip_rules_insert AFTER INSERT ON ip_rules BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('ip_rules',new.id,'INSERT'); END;
CREATE TRIGGER IF NOT EXISTS audit_ip_rules_delete AFTER DELETE ON ip_rules BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('ip_rules',old.id,'DELETE'); END;
CREATE TRIGGER IF NOT EXISTS audit_instances_insert AFTER INSERT ON instances BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('instances',new.id,'INSERT'); END;
CREATE TRIGGER IF NOT EXISTS audit_instances_update AFTER UPDATE ON instances WHEN new.name<>old.name OR new.transport<>old.transport OR new.config<>old.config OR new.secret<>old.secret OR new.enabled<>old.enabled BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('instances',new.id,'UPDATE'); END;
CREATE TRIGGER IF NOT EXISTS audit_instances_delete AFTER DELETE ON instances BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('instances',old.id,'DELETE'); END;
INSERT OR IGNORE INTO schema_migrations(version) VALUES(1);
INSERT OR IGNORE INTO schema_migrations(version) VALUES(5);
INSERT OR IGNORE INTO schema_migrations(version) VALUES(6);
INSERT OR IGNORE INTO schema_migrations(version) VALUES(7);

INSERT OR IGNORE INTO schema_migrations(version) VALUES(8);

CREATE TABLE IF NOT EXISTS access_roles(id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,description TEXT NOT NULL DEFAULT '',version INTEGER NOT NULL DEFAULT 1,all_mcp INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS role_instances(role_id TEXT NOT NULL REFERENCES access_roles(id) ON DELETE CASCADE,instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,PRIMARY KEY(role_id,instance_id));
CREATE TABLE IF NOT EXISTS user_roles(role_id TEXT NOT NULL REFERENCES access_roles(id) ON DELETE CASCADE,user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,PRIMARY KEY(role_id,user_id));
INSERT OR IGNORE INTO schema_migrations(version) VALUES(9);

CREATE TABLE IF NOT EXISTS role_tools(role_id TEXT NOT NULL,instance_id TEXT NOT NULL,tool_name TEXT NOT NULL CHECK(length(tool_name)>0),PRIMARY KEY(role_id,instance_id,tool_name),FOREIGN KEY(role_id,instance_id) REFERENCES role_instances(role_id,instance_id) ON DELETE CASCADE);
INSERT OR IGNORE INTO schema_migrations(version) VALUES(10);

INSERT OR IGNORE INTO schema_migrations(version) VALUES(11);
