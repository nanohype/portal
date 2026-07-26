ALTER TABLE clusters DROP CONSTRAINT clusters_name_key;
ALTER TABLE clusters ADD CONSTRAINT clusters_org_id_name_key UNIQUE (org_id, name);
