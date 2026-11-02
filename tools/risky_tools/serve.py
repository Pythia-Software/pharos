#!/usr/bin/env python3
"""Serve one private research HTML artifact over loopback, with no write API."""
import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('report',type=Path)
    parser.add_argument('--port',type=int,default=56393)
    args=parser.parse_args();report=args.report.expanduser().resolve(strict=True)
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.headers.get('Host') not in {f'127.0.0.1:{self.server.server_port}',f'localhost:{self.server.server_port}'}:
                self.send_error(403);return
            if self.path=='/favicon.ico':
                self.send_response(204);self.end_headers();return
            if self.path not in {'/','/queue.html'}:
                self.send_error(404);return
            content=report.read_bytes();self.send_response(200)
            for key,value in [('Content-Type','text/html; charset=utf-8'),('Content-Length',str(len(content))),('Cache-Control','no-store'),('X-Content-Type-Options','nosniff')]:self.send_header(key,value)
            self.end_headers();self.wfile.write(content)
        def log_message(self,*args):pass
    server=ThreadingHTTPServer(('127.0.0.1',args.port),Handler)
    print(f'http://127.0.0.1:{server.server_port}/queue.html',flush=True)
    server.serve_forever()


if __name__=='__main__':main()
