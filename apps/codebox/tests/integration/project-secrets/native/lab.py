#!/usr/bin/env python3
"""Own-port DNS/TLS lab. Persist hashes only; never raw request headers."""
import argparse, hashlib, json, socket, ssl, struct, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def dns_reply(data, ip):
    if len(data) < 12: return None
    off, labels = 12, []
    try:
        while data[off]:
            n = data[off]
            if n > 63: return None
            labels.append(data[off+1:off+1+n].decode('ascii').lower()); off += n+1
        off += 1
        typ, cls = struct.unpack('>HH', data[off:off+4])
        q = data[12:off+4]
        known = '.'.join(labels) in ('p02lab.test', 'evil.test')
        if known and typ == 1 and cls == 1:
            return data[:2] + struct.pack('>HHHHH',0x8180,1,1,0,0) + q + b'\xc0\x0c' + struct.pack('>HHIH',1,1,1,4) + socket.inet_aton(ip)
        return data[:2] + struct.pack('>HHHHH',0x8180 if known else 0x8185,1,0,0,0) + q
    except (IndexError, ValueError, struct.error, UnicodeError): return None


def main():
    p=argparse.ArgumentParser();p.add_argument('--dir',required=True);p.add_argument('--ip',required=True);p.add_argument('--tls',type=int,required=True);p.add_argument('--dns',type=int,required=True);a=p.parse_args()
    lock=threading.Lock()
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            # Do not echo or save raw canaries, URLs are fixed by the probe.
            paths={'/headers','/basic','/evil-direct','/redirect-to-evil','/redirect-target','/rotation','/removed','/remaining','/c-positive','/c-wrong-host','/ab-wrong-host','/after-refused-readd','/queued-not-live','/nextboot-ab','/nextboot-c','/nextboot-ab-wrong-host','/nextboot-c-wrong-host'}
            host=self.headers.get('Host','').split(':')[0]
            r={'path':self.path if self.path in paths else '<other>','host':host if host in ('p02lab.test','evil.test') else '<other>','headers':{k:hashlib.sha256(self.headers.get(k,'').encode()).hexdigest() for k in ('x-a','x-b','x-c','authorization')}}
            with lock, open(a.dir+'/requests.jsonl','a') as f: f.write(json.dumps(r)+'\n')
            self.send_response(302 if self.path=='/redirect-to-evil' else 200)
            if self.path=='/redirect-to-evil': self.send_header('Location',f'https://evil.test:{a.tls}/redirect-target')
            self.send_header('Content-Length','0');self.end_headers()
        def log_message(self,*args): pass
    # Binding is authoritative preflight: no SO_REUSEADDR; occupied ports reject.
    class Server(ThreadingHTTPServer): allow_reuse_address=False
    http=Server(('0.0.0.0',a.tls),Handler)
    udp=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);udp.bind(('0.0.0.0',a.dns))
    ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.load_cert_chain(a.dir+'/srv.crt',a.dir+'/srv.key');http.socket=ctx.wrap_socket(http.socket,server_side=True)
    def dns():
        while True:
            data,addr=udp.recvfrom(512);reply=dns_reply(data,a.ip)
            if reply: udp.sendto(reply,addr)
    threading.Thread(target=dns,daemon=True).start()
    open(a.dir+'/lab-ready','x').close()
    http.serve_forever()
if __name__=='__main__': main()
