-- +migrate Up
CREATE TABLE IF NOT EXISTS idm_usr_meta (
    uuid          	VARCHAR(255) NOT NULL,
    node_uuid		VARCHAR(255) NOT NULL,
    namespace		VARCHAR(255) NOT NULL,
    owner 			VARCHAR(255),
    timestamp 		INT(11),
    format 			VARCHAR(50),
    data 			BLOB,
    PRIMARY KEY (uuid),
    UNIQUE (namespace,node_uuid,owner)
);
CREATE INDEX idm_usr_meta_node_idx ON idm_usr_meta (node_uuid);

-- +migrate Down
DROP TABLE idm_usr_meta;
