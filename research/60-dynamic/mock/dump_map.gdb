set pagination off
break *0x44f8e6
run -c /tmp/ajs-lab/ajs.conf connect Test*
x/32xb $rsi
x/s $rsi
x/s *(char**)$rsi
continue
