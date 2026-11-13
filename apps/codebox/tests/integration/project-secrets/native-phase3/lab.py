#!/usr/bin/env python3
"""Own-port DNS/TLS lab for the Phase 3 qualification. Persist hashes only;
never raw request headers.

DNS: authoritative for the .test zone used by the run (p3lab.test and
evil.test both answer with the host LAN IP; any other .test name is REFUSED;
AAAA for a known name is NODATA). Non-.test names are relayed to an upstream
resolver so the disposable guest can fetch its own throwaway packages
(apk/npm/pip); the relay never rewrites an answer and is not part of the host
resolver configuration.

TLS: one server on the chosen port, presenting the run's one-day temporary
certificate for p3lab.test (SAN also covers evil.test and the LAN IP). Every
GET is answered with an empty 200 and recorded as SHA256 digests of the
header values only.
"""
import argparse, hashlib, json, socket, ssl, struct, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TEST_ZONE = ('p3lab.test', 'evil.test')


def dns_reply(data, ip):
    """Answer a query from the .test zone, or return None.

    None means "not handled locally": the caller relays non-.test names to
    the upstream resolver and drops malformed packets. Unknown .test names
    are REFUSED here, never forwarded.
    """
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
        name = '.'.join(labels)
        if not name.endswith('.test'):
            return None
        known = name in TEST_ZONE
        if known and typ == 1 and cls == 1:
            return data[:2] + struct.pack('>HHHHH',0x8180,1,1,0,0) + q + b'\xc0\x0c' + struct.pack('>HHIH',1,1,1,4) + socket.inet_aton(ip)
        return data[:2] + struct.pack('>HHHHH',0x8180 if known else 0x8185,1,0,0,0) + q
    except (IndexError, ValueError, struct.error, UnicodeError): return None


def main():
    p=argparse.ArgumentParser();p.add_argument('--dir',required=True);p.add_argument('--ip',required=True);p.add_argument('--tls',type=int,required=True);p.add_argument('--dns',type=int,required=True);p.add_argument('--dns-upstream',default='');a=p.parse_args()
    lock=threading.Lock()
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            # Do not echo or save raw canaries, URLs are fixed by the probe.
            paths={'/node-api','/python-root','/evil-direct'}
            host=self.headers.get('Host','').split(':')[0]
            r={'path':self.path if self.path in paths else '<other>','host':host if host in TEST_ZONE else '<other>','headers':{k:hashlib.sha256(self.headers.get(k,'').encode()).hexdigest() for k in ('x-api-key',)}}
            with lock, open(a.dir+'/requests.jsonl','a') as f: f.write(json.dumps(r)+'\n')
            self.send_response(200)
            self.send_header('Content-Length','0');self.end_headers()
        def log_message(self,*args): pass
    # Binding is authoritative preflight: no SO_REUSEADDR; occupied ports reject.
    class Server(ThreadingHTTPServer): allow_reuse_address=False
    http=Server(('0.0.0.0',a.tls),Handler)
    udp=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);udp.bind(('0.0.0.0',a.dns))
    ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.load_cert_chain(a.dir+'/srv.crt',a.dir+'/srv.key');http.socket=ctx.wrap_socket(http.socket,server_side=True)
    def relay(data,addr):
        # Forward a non-.test query to the upstream resolver verbatim and
        # relay the answer. Best-effort: a failed relay simply drops, and the
        # guest's resolver retries.
        try:
            with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as fwd:
                fwd.settimeout(4)
                fwd.sendto(data,(a.dns_upstream,53))
                resp,_=fwd.recvfrom(4096)
                udp.sendto(resp,addr)
        except OSError: pass
    def dns():
        while True:
            data,addr=udp.recvfrom(512);reply=dns_reply(data,a.ip)
            if reply: udp.sendto(reply,addr)
            elif a.dns_upstream: threading.Thread(target=relay,args=(data,addr),daemon=True).start()
    threading.Thread(target=dns,daemon=True).start()
    open(a.dir+'/lab-ready','x').close()
    http.serve_forever()
if __name__=='__main__': main()
