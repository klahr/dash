-- Custom scans users write in Dash (model.ScanTemplate), one per project and name.
CREATE TABLE scan_templates (
	project_id TEXT NOT NULL,
	name       TEXT NOT NULL,
	data       TEXT NOT NULL,
	PRIMARY KEY (project_id, name)
);
