#!/usr/bin/env python
#
# srndv2 reference frontend
#
from . import overchan
import logging
from tornado import ioloop, web

def main():
    import argparse
    ap = argparse.ArgumentParser()
    ap.add_argument('--bind', type=int, default=8383)
    ap.add_argument('--name', type=str, default='nntpchan')
    ap.add_argument('--debug', action='store_const', const=True, default=False)
    args = ap.parse_args()
    
    loglvl = args.debug and logging.DEBUG or logging.INFO
    logging.basicConfig(level=loglvl)

    loop = ioloop.IOLoop.instance()

    frontend = overchan.Frontend(loop)

    context = {'srndapi': frontend}
    
    app = web.Application([
        (r"/newsgroup/(.*)", overchan.NewsgroupHandler, context),
        (r"/thread/(.*)", overchan.ThreadHandler, context),
        (r"/post", overchan.PostHandler, context),
    ])

    app.listen(args.bind)
    frontend.run()
    loop.start()
    
if __name__ == '__main__':
    main()