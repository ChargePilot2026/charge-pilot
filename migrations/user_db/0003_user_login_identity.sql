-- Serialize first login without rewriting or silently merging existing user rows.
CREATE TABLE IF NOT EXISTS user_login_identity (
  openid VARBINARY(64) NOT NULL,
  PRIMARY KEY (openid)
) ENGINE=InnoDB;
