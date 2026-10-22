set pagination off
break *0x418e3b
run -c /tmp/ajs-lab/ajs.conf connect Test*
x/80xb $r12
info registers r12 rbx
continue
