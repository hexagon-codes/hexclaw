"""运维集成验证用的进程边界，数据与 HTTP 读写实际发生于隔离容器。"""

import http.server
import json
import os
from pathlib import Path
import signal
import sqlite3
import time

home = Path('/data/.hexclaw')
home.mkdir(parents=True, exist_ok=True)
database = home / 'data.db'
connection = sqlite3.connect(database)
connection.execute('PRAGMA journal_mode=WAL')
connection.execute('PRAGMA wal_autocheckpoint=0')
connection.execute('CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY)')
connection.execute('INSERT OR IGNORE INTO schema_migrations VALUES (1)')
connection.execute('CREATE TABLE IF NOT EXISTS receipts(id TEXT PRIMARY KEY, state TEXT)')
connection.execute("INSERT OR IGNORE INTO receipts VALUES ('original-unknown', 'unknown')")
connection.commit()
if os.environ.get('FIXTURE_CRASH') == '1':
    connection.execute('INSERT OR IGNORE INTO schema_migrations VALUES (2)')
    connection.execute("INSERT OR IGNORE INTO receipts VALUES ('new-process', 'unknown')")
    connection.commit()
    os._exit(2)

def stop(_signal, _frame):
    (home / 'stopping').write_text('yes')
    time.sleep(2)
    # 模拟已提交但尚未 checkpoint 的 WAL，恢复必须同时保存数据库与 WAL。
    os._exit(0)

signal.signal(signal.SIGTERM, stop)

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_GET(self):
        if self.path != '/health' and self.headers.get('Authorization') != 'Bearer fixture-only':
            self.send_response(401)
            self.end_headers()
            return
        body = {'/health': {'status': 'healthy'},
                '/api/v1/config': {'backend_id': 'isolated-backend'},
                '/api/v1/knowledge/documents': {'documents': [{'id': 'original-doc'}]}}.get(self.path)
        self.send_response(200 if body is not None else 404)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps(body).encode())

http.server.HTTPServer(('0.0.0.0', 16060), Handler).serve_forever()
