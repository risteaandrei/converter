CREATE DATABASE auth;

\c auth;

CREATE TABLE "users" (
    id SERIAL PRIMARY KEY,
    email varchar(255) NOT NULL,
    password VARCHAR(255) NOT NULL
);

INSERT INTO "users" (email, password) VALUES ('foo@bar.com', 'foobarpass');