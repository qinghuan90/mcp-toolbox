package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"toolbox/internal/storage"
)

var tables = []string{"users", "instances", "sessions", "tokens", "config_versions", "rules", "logs", "tasks", "discoveries", "management_audit"}

func main() {
	destination := flag.String("sqlite", "", "new SQLite destination file")
	flag.Parse()
	if *destination == "" { fatal(errors.New("-sqlite is required")) }
	if _, err := os.Stat(*destination); err == nil { fatal(errors.New("destination already exists; migration never overwrites data")) } else if !os.IsNotExist(err) { fatal(err) }
	source := os.Getenv("PG_SOURCE_URL")
	if source == "" { fatal(errors.New("PG_SOURCE_URL is required")) }
	key, err := base64.StdEncoding.DecodeString(os.Getenv("MASTER_KEY"))
	if err != nil || len(key) != 32 { fatal(errors.New("existing MASTER_KEY must be supplied for ciphertext verification")) }
	block, err := aes.NewCipher(key)
	if err != nil { fatal(err) }
	aead, err := cipher.NewGCM(block)
	if err != nil { fatal(err) }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pg, err := pgxpool.New(ctx, source)
	if err != nil { fatal(err) }
	defer pg.Close()
	pgTx, err := pg.BeginTx(ctx, pgx.TxOptions{IsoLevel:pgx.RepeatableRead, AccessMode:pgx.ReadOnly})
	if err != nil { fatal(err) }
	defer pgTx.Rollback(ctx)
	db, err := storage.Open(*destination)
	if err != nil { fatal(err) }
	defer db.Close()
	sqlTx, err := db.DB.BeginTx(ctx,nil)
	if err != nil { fatal(err) }
	defer sqlTx.Rollback()
	for _, table := range tables {
		if err := copyTable(ctx, pgTx, sqlTx, table, aead); err != nil { fatal(fmt.Errorf("%s: %w", table, err)) }
	}
	if err = sqlTx.Commit(); err != nil { fatal(err) }
	var integrity string
	if err = db.DB.QueryRowContext(ctx,"PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" { fatal(fmt.Errorf("SQLite integrity check: %s %v",integrity,err)) }
	if err = pgTx.Commit(ctx); err != nil { fatal(err) }
	fmt.Println("Migration complete. PostgreSQL source remains unchanged. Table counts:")
	for _, table := range tables { n,_:=db.Count(ctx,table); fmt.Printf("%s=%d\n",table,n) }
}

func copyTable(ctx context.Context, pgTx pgx.Tx, sqlTx *sql.Tx, table string, aead cipher.AEAD) error {
	if table=="management_audit" {
		if _,err:=sqlTx.ExecContext(ctx,"DELETE FROM management_audit");err!=nil{return err}
	}
	var existing int64
	if err := sqlTx.QueryRowContext(ctx,"SELECT count(*) FROM "+table).Scan(&existing); err != nil { return err }
	if existing != 0 { return errors.New("destination table is not empty") }
	rows, err := pgTx.Query(ctx, "SELECT column_name,data_type FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 ORDER BY ordinal_position",table)
	if err != nil { return err }
	var cols, expr []string
	for rows.Next() {
		var col, typ string
		if err=rows.Scan(&col,&typ); err!=nil {rows.Close(); return err}
		if !validIdentifier(col) {rows.Close(); return errors.New("invalid source column")}
		cols=append(cols,col)
		if typ=="jsonb" || typ=="json" {expr=append(expr,"\""+col+"\"::text")} else {expr=append(expr,"\""+col+"\"")}
	}
	if err=rows.Err();err!=nil {rows.Close();return err}
	rows.Close()
	if len(cols)==0 {return errors.New("source table missing")}
	query:="SELECT "+strings.Join(expr,",")+" FROM \""+table+"\""
	sourceRows,err:=pgTx.Query(ctx,query)
	if err!=nil{return err}
	defer sourceRows.Close()
	placeholders:=make([]string,len(cols)); for i:=range placeholders {placeholders[i]="?"}
	insert:="INSERT INTO \""+table+"\"(\""+strings.Join(cols,"\",\"")+"\") VALUES("+strings.Join(placeholders,",")+")"
	var copied int64
	for sourceRows.Next() {
		values,scanErr:=sourceRows.Values(); if scanErr!=nil{return scanErr}
		for i,v:=range values {if t,ok:=v.(time.Time);ok {values[i]=t.UTC().Format(time.RFC3339Nano)}}
		if table=="instances" || table=="config_versions" || table=="discoveries" {
			for i,col:=range cols {if col=="secret" && values[i]!=nil {
				blob,ok:=values[i].([]byte); if !ok {return errors.New("secret is not binary")}
				if len(blob)<aead.NonceSize() {return errors.New("encrypted secret is too short")}
				plain,decryptErr:=aead.Open(nil,blob[:aead.NonceSize()],blob[aead.NonceSize():],nil)
				if decryptErr!=nil || !json.Valid(plain) {return errors.New("MASTER_KEY cannot decrypt an existing secret")}
			}}
		}
		if _,err=sqlTx.ExecContext(ctx,insert,values...);err!=nil{return err}
		copied++
	}
	if err=sourceRows.Err();err!=nil{return err}
	sourceRows.Close()
	var targetCount,sourceCount int64
	if err=sqlTx.QueryRowContext(ctx,"SELECT count(*) FROM "+table).Scan(&targetCount);err!=nil{return err}
	if err=pgTx.QueryRow(ctx,"SELECT count(*) FROM \""+table+"\"").Scan(&sourceCount);err!=nil{return err}
	if copied!=sourceCount || targetCount!=sourceCount {return fmt.Errorf("count mismatch source=%d copied=%d target=%d",sourceCount,copied,targetCount)}
	return nil
}

func validIdentifier(s string) bool {
	if s=="" {return false}
	for _,r:=range s {if !(r=='_'||r>='a'&&r<='z'||r>='0'&&r<='9'){return false}}
	return true
}

func fatal(err error) { fmt.Fprintln(os.Stderr,"migration failed:",err); os.Exit(1) }
